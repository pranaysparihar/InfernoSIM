package agentrunner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"infernosim/pkg/agentreliability"
)

func TestPythonCheckerOptionOperandsAndModuleScope(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python unavailable")
	}
	for _, prefix := range [][]string{{"-W", "ignore"}, {"-X", "utf8"}, {"-B"}, {"--"}} {
		o := checkerFixture(t, t.TempDir())
		o.Command = append(append([]string{python}, prefix...), o.Command[1:]...)
		before, err := o.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if before.Files["script"] == "" {
			t.Fatal("interpreter flag hid the script")
		}
		os.WriteFile(filepath.Join(filepath.Dir(o.Manifest), "checker.py"), []byte("weakened"), 0600)
		if err := o.VerifyFingerprint(before); err == nil {
			t.Fatal("edited script passed behind interpreter option")
		}
	}
	o := checkerFixture(t, t.TempDir())
	o.Manifest = ""
	for _, args := range [][]string{{"-m", "checker"}, {"-mchecker"}} {
		o.Command = append([]string{python}, args...)
		if _, err := o.Fingerprint(); err == nil {
			t.Fatal("module source was not pinned or declared")
		}
	}
}

func TestReductionAndComparisonRejectUnpinnedOrChangedChecker(t *testing.T) {
	o := checkerFixture(t, t.TempDir())
	fingerprint, err := o.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	c := agentreliability.Case{ID: "regression", FaultID: "fault"}
	good := Result{Case: c, ScopeHash: "scope", Checker: fingerprint, StateCheckHash: fingerprint.Digest, Assertions: []agentreliability.AssertionResult{{ID: "state:database", Coverage: "violated"}}}
	for _, mode := range []string{"legacy", "hash-mismatch", "disappeared", "appeared"} {
		t.Run(mode, func(t *testing.T) {
			n := 0
			run := func(context.Context, agentreliability.Case) (Result, error) {
				n++
				r := good
				switch mode {
				case "legacy":
					r.Checker = nil
					r.CheckerStatus = "legacy-unpinned"
				case "hash-mismatch":
					r.StateCheckHash = "forged"
				case "disappeared":
					if n == 2 {
						r.Checker = nil
						r.StateCheckHash = ""
					}
				case "appeared":
					if n == 1 {
						r.Checker = nil
						r.StateCheckHash = ""
					}
				}
				return r, nil
			}
			if _, err := ReduceFaults(context.Background(), c, "state:database", 4, run); err == nil {
				t.Fatal("unproven checker allowed in reduction")
			}
			n = 0
			if _, err := MinimizeTrial(context.Background(), Trial{Case: c}, "state:database", 4, func(ctx context.Context, trial Trial) (Result, error) { return run(ctx, trial.Case) }); err == nil {
				t.Fatal("unproven checker allowed in minimization")
			}
		})
	}
	bad := good
	bad.StateCheckHash = "forged"
	if _, err := CompareResults([]Result{bad}, []Result{bad}); err == nil {
		t.Fatal("matching forged hashes bypassed fingerprint comparison")
	}
}

func checkerFixture(t *testing.T, root string) StateCheckOptions {
	t.Helper()
	for name, data := range map[string]string{"checker.py": "import helper\n", "helper.py": "CHECK = True\n", "settings.json": "{\"limit\":1}", "deps.lock": "library=1", "destination.db": "mutable"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := CheckerManifest{Version: 1, Scope: "declared-inputs", Files: map[string]string{"helper": "helper.py", "settings": "settings.json", "lock": "deps.lock"}, Versions: map[string]string{"runtime": "test-1"}, SecretRevisions: map[string]string{"test-key": "opaque-revision-7"}}
	b, _ := json.Marshal(m)
	path := filepath.Join(root, "checker.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return StateCheckOptions{Command: []string{exe, filepath.Join(root, "checker.py"), "--check"}, IDs: []string{"database"}, Timeout: time.Second, Manifest: path}
}

func TestCheckerPinRejectsCodeDependencyConfigAndContractChanges(t *testing.T) {
	for _, name := range []string{"checker.py", "helper.py", "settings.json", "deps.lock", "checker.json", "args", "ids", "timeout"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			o := checkerFixture(t, root)
			before, err := o.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			if err = before.Validate(); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "args":
				o.Command = append(o.Command, "--weaken")
			case "ids":
				o.IDs = []string{"other"}
			case "timeout":
				o.Timeout = 2 * time.Second
			case "checker.json":
				b, _ := os.ReadFile(o.Manifest)
				b = []byte(strings.Replace(string(b), "test-1", "test-2", 1))
				os.WriteFile(o.Manifest, b, 0600)
			default:
				os.WriteFile(filepath.Join(root, name), []byte("weakened"), 0600)
			}
			if err = o.VerifyFingerprint(before); err == nil {
				t.Fatal("changed checker accepted")
			}
			after, err := o.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			r := Reproduction{Trial: Trial{Case: agentreliability.Case{ID: "original"}}, Checker: before}
			fork, err := NewCheckerCase(r, after)
			if err != nil {
				t.Fatal(err)
			}
			if fork.CaseIdentity == CaseIdentity(r.Trial, before) || fork.ParentCase != CaseIdentity(r.Trial, before) || r.Checker.Digest != before.Digest {
				t.Fatal("case lineage lost")
			}
		})
	}
}

func TestCheckerRelocationAndMutableDestination(t *testing.T) {
	a := checkerFixture(t, t.TempDir())
	b := checkerFixture(t, t.TempDir())
	x, err := a.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err = b.VerifyFingerprint(x); err != nil {
		t.Fatalf("relocation changed fingerprint: %v", err)
	}
	if err = os.WriteFile(filepath.Join(filepath.Dir(b.Manifest), "destination.db"), []byte("duplicate"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = b.VerifyFingerprint(x); err != nil {
		t.Fatal("destination was included in checker identity", err)
	}
	b.Manifest = ""
	partial, err := b.Fingerprint()
	if err != nil || partial.Scope != "direct-inputs-only" {
		t.Fatal(partial, err)
	}
	if err = b.VerifyFingerprint(x); err == nil {
		t.Fatal("missing manifest accepted")
	}
}

func TestCheckerMissingAndTamperedInputs(t *testing.T) {
	o := checkerFixture(t, t.TempDir())
	f, err := o.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	f.Contract = "tampered"
	if err = f.Validate(); err == nil {
		t.Fatal("tampered metadata accepted")
	}
	if err = os.Remove(filepath.Join(filepath.Dir(o.Manifest), "helper.py")); err != nil {
		t.Fatal(err)
	}
	if _, err = o.Fingerprint(); err == nil {
		t.Fatal("missing dependency accepted")
	}
}

func TestCheckerPublicEnvironmentAndSecretRevision(t *testing.T) {
	o := checkerFixture(t, t.TempDir())
	b, _ := os.ReadFile(o.Manifest)
	var m CheckerManifest
	json.Unmarshal(b, &m)
	m.PublicEnvironment = []string{"INFERNOSIM_TEST_CHECK_SETTING"}
	b, _ = json.Marshal(m)
	os.WriteFile(o.Manifest, b, 0600)
	t.Setenv("INFERNOSIM_TEST_CHECK_SETTING", "public-one")
	t.Setenv("INFERNOSIM_UNDECLARED_SECRET", "do-not-include")
	f, err := o.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(f)
	if strings.Contains(string(data), "do-not-include") || strings.Contains(string(data), "public-one") {
		t.Fatal("environment leaked")
	}
	t.Setenv("INFERNOSIM_UNDECLARED_SECRET", "changed")
	if err = o.VerifyFingerprint(f); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INFERNOSIM_TEST_CHECK_SETTING", "public-two")
	if err = o.VerifyFingerprint(f); err == nil {
		t.Fatal("declared config ignored")
	}
}

func TestLegacyCheckerStatusInReports(t *testing.T) {
	r := ReportingResult([]Result{{Passed: true, StateCheckHash: "legacy", Case: agentreliability.Case{ID: "old"}}})
	if !strings.Contains(r.Cases[0].Message, "legacy-unpinned") || len(r.Findings) != 1 {
		t.Fatal("legacy status hidden", r)
	}
	if _, err := CompareResults([]Result{{StateCheckHash: "legacy", Case: agentreliability.Case{ID: "old"}}}, []Result{{StateCheckHash: "legacy", Case: agentreliability.Case{ID: "old"}}}); err == nil {
		t.Fatal("legacy comparison accepted")
	}
}
