package agentrunner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"infernosim/pkg/causal"
)

// CheckerManifest is caller-supplied, inert input. Files include code, imported
// helpers, lockfiles and non-secret config, never the destination being checked.
// Runtime/package versions and secret configuration revisions are declarations;
// InfernoSIM does not execute package managers or inspect unrelated environment.
type CheckerManifest struct {
	Version           int               `json:"version"`
	Files             map[string]string `json:"files"`
	Versions          map[string]string `json:"versions,omitempty"`
	PublicEnvironment []string          `json:"public_environment,omitempty"`
	SecretRevisions   map[string]string `json:"secret_revisions,omitempty"`
	Scope             string            `json:"scope"`
}

type CheckerFingerprint struct {
	Version      int               `json:"version"`
	Digest       string            `json:"digest"`
	Scope        string            `json:"scope"`
	Files        map[string]string `json:"files"`
	Contract     string            `json:"contract"`
	Declarations string            `json:"declarations"`
}

func (f *CheckerFingerprint) Validate() error {
	if f == nil {
		return nil
	}
	if f.Version != 1 || (f.Scope != "direct-inputs-only" && f.Scope != "declared-inputs") || len(f.Files) == 0 || len(f.Files) > 130 {
		return fmt.Errorf("invalid checker fingerprint scope or version")
	}
	for name, hash := range f.Files {
		if name == "" || len(hash) != 64 {
			return fmt.Errorf("invalid checker fingerprint file")
		}
	}
	c := *f
	c.Digest = ""
	if f.Digest != causal.Hash(c) {
		return fmt.Errorf("checker fingerprint integrity mismatch")
	}
	return nil
}

func hashCheckerFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("checker input unavailable: %w", err)
	}
	defer f.Close()
	i, err := f.Stat()
	if err != nil || !i.Mode().IsRegular() || i.Size() > 256<<20 {
		return "", fmt.Errorf("checker input must be a regular file <=256 MiB")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, 256<<20+1))
	if err != nil || n > 256<<20 {
		return "", fmt.Errorf("checker input exceeds size limit or cannot be read")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Fingerprint captures the actual executable, a direct interpreter script, and
// explicitly declared dependencies. Arbitrary import closure is not inferred.
func (o StateCheckOptions) Fingerprint() (*CheckerFingerprint, error) {
	if len(o.Command) == 0 {
		return nil, nil
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	f := &CheckerFingerprint{Version: 1, Scope: "direct-inputs-only", Files: map[string]string{}}
	aliases := map[string]string{}
	add := func(name, path string) error {
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return fmt.Errorf("missing checker input %s", name)
		}
		h, err := hashCheckerFile(resolved)
		if err != nil {
			return err
		}
		f.Files[name] = h
		aliases[abs] = "@" + name
		aliases[resolved] = "@" + name
		return nil
	}
	m := CheckerManifest{}
	if o.Manifest != "" {
		data, err := readRegular(o.Manifest, 1<<20)
		if err != nil {
			return nil, err
		}
		if err := decodeStrict(data, &m); err != nil {
			return nil, fmt.Errorf("checker manifest: %w", err)
		}
		if m.Version != 1 || m.Scope != "declared-inputs" || len(m.Files) == 0 || len(m.Files) > 128 || len(m.PublicEnvironment) > 64 || len(m.Versions) > 128 || len(m.SecretRevisions) > 64 {
			return nil, fmt.Errorf("checker manifest requires version 1, scope declared-inputs and 1..128 files")
		}
		keys := make([]string, 0, len(m.Files))
		for k := range m.Files {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, name := range keys {
			if !checkID.MatchString(name) || name == "executable" || name == "script" {
				return nil, fmt.Errorf("invalid/reserved checker input name")
			}
			path := m.Files[name]
			if filepath.IsAbs(path) {
				return nil, fmt.Errorf("checker manifest files must be relative to manifest")
			}
			if err := add(name, filepath.Join(filepath.Dir(o.Manifest), path)); err != nil {
				return nil, err
			}
		}
		f.Scope = "declared-inputs"
	}
	program, err := exec.LookPath(o.Command[0])
	if err != nil {
		return nil, fmt.Errorf("checker executable unavailable: %w", err)
	}
	if err = add("executable", program); err != nil {
		return nil, err
	}
	// Resolve the first script operand, including Python flags with separate
	// operands. Inline source remains part of the argument contract; module
	// execution needs a declaration of the module's actual files.
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(o.Command[0])), ".exe")
	python := strings.HasPrefix(base, "python")
	for i := 1; i < len(o.Command); i++ {
		arg := o.Command[i]
		if arg == "-c" || arg == "-m" || (python && (strings.HasPrefix(arg, "-c") || strings.HasPrefix(arg, "-m"))) {
			if python && strings.HasPrefix(arg, "-m") && o.Manifest == "" {
				return nil, fmt.Errorf("Python module checkers require a dependency manifest containing the module source")
			}
			break
		}
		if python && (arg == "-W" || arg == "-X") {
			i++
			if i >= len(o.Command) {
				return nil, fmt.Errorf("missing Python checker option operand")
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(arg))
		if python || ext == ".py" || ext == ".sh" || ext == ".js" || ext == ".rb" {
			if err = add("script", arg); err != nil {
				return nil, err
			}
		}
		break
	}
	argv := append([]string(nil), o.Command...)
	argv[0] = "@executable"
	for i := 1; i < len(argv); i++ {
		abs, err := filepath.Abs(argv[i])
		if err == nil {
			if alias, ok := aliases[abs]; ok {
				argv[i] = alias
			}
		}
	}
	ids := append([]string(nil), o.IDs...)
	sort.Strings(ids)
	f.Contract = causal.Hash(struct {
		Args, IDs []string
		Timeout   int64
		Setup     bool
	}{argv, ids, int64(o.timeout()), len(o.SetupCommand) > 0})
	env := map[string]string{}
	for _, name := range m.PublicEnvironment {
		if !checkID.MatchString(name) {
			return nil, fmt.Errorf("invalid public environment name")
		}
		v, ok := os.LookupEnv(name)
		for _, entry := range o.Environment {
			key, value, exists := strings.Cut(entry, "=")
			if exists && key == name {
				v = value
				ok = true
			}
		}
		if !ok {
			return nil, fmt.Errorf("declared public checker environment missing: %s", name)
		}
		env[name] = v
	}
	f.Declarations = causal.Hash(struct{ Versions, PublicEnvironment, SecretRevisions map[string]string }{m.Versions, env, m.SecretRevisions})
	f.Digest = causal.Hash(f)
	return f, nil
}

func (o StateCheckOptions) VerifyFingerprint(expected *CheckerFingerprint) error {
	if expected == nil {
		return nil
	}
	if err := expected.Validate(); err != nil {
		return err
	}
	actual, err := o.Fingerprint()
	if err != nil {
		return err
	}
	if actual == nil || actual.Digest != expected.Digest {
		return fmt.Errorf("checker fingerprint changed: create a new case with --new-case-out; this cannot verify the original case")
	}
	return nil
}

func checkerStatus(f *CheckerFingerprint, hasChecks bool) string {
	if f != nil {
		return "pinned/" + f.Scope
	}
	if hasChecks {
		return "legacy-unpinned"
	}
	return "no-state-checker"
}

func resultCheckerDigest(r Result) (string, error) {
	if r.CheckerStatus == "legacy-unpinned" || (r.StateCheckHash != "" && r.Checker == nil) {
		return "", fmt.Errorf("legacy-unpinned checker: create a new pinned case before comparison or reduction")
	}
	if err := r.Checker.Validate(); err != nil {
		return "", err
	}
	if r.Checker == nil {
		return "", nil
	}
	if r.StateCheckHash != r.Checker.Digest {
		return "", fmt.Errorf("result checker digest disagrees with its fingerprint")
	}
	return r.Checker.Digest, nil
}

// CaseIdentity includes the verification contract without modifying selectors
// or the original trial ID used by the schedule/fault engine.
func CaseIdentity(trial Trial, checker *CheckerFingerprint) string {
	return "case_" + causal.Hash(struct {
		Trial   Trial
		Checker *CheckerFingerprint
	}{trial, checker})[:24]
}

// NewCheckerCase forks inert evidence explicitly; no retained assertion result
// or execution proof is claimed to have been verified by the changed checker.
func NewCheckerCase(source Reproduction, fingerprint *CheckerFingerprint) (Reproduction, error) {
	if fingerprint == nil {
		return source, fmt.Errorf("new checker case requires an explicit checker")
	}
	if err := fingerprint.Validate(); err != nil {
		return source, err
	}
	if source.Checker != nil && source.Checker.Digest == fingerprint.Digest {
		return source, fmt.Errorf("checker is unchanged")
	}
	parent := source.CaseIdentity
	if parent == "" {
		parent = CaseIdentity(source.Trial, source.Checker)
	}
	source.ParentCase = parent
	source.Checker = fingerprint
	source.CaseIdentity = CaseIdentity(source.Trial, fingerprint)
	source.CheckerChange = "explicit checker revision; source outcome does not establish this case's outcome"
	return source, nil
}
