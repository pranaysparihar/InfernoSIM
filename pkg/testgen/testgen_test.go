package testgen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGenerateAllFrameworks(t *testing.T) {
	incident := t.TempDir()
	if err := os.WriteFile(filepath.Join(incident, "inbound.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, framework := range []string{FrameworkGoTestcontainers, FrameworkDockerCompose, FrameworkGitHubActions} {
		t.Run(framework, func(t *testing.T) {
			frameworkIncident := incident
			if framework == FrameworkGitHubActions {
				var err error
				frameworkIncident, err = os.MkdirTemp(".", ".testgen-incident-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(frameworkIncident) })
				if err := os.WriteFile(filepath.Join(frameworkIncident, "inbound.log"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			out := t.TempDir()
			result, err := Generate(Options{IncidentDir: frameworkIncident, OutputDir: out, Framework: framework, Image: "infernosim:test"})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Files) == 0 {
				t.Fatal("no generated files")
			}
			for _, path := range result.Files {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
					t.Fatalf("mode=%o", info.Mode().Perm())
				}
				content, _ := os.ReadFile(path)
				if strings.HasSuffix(path, ".go") {
					if _, err := parser.ParseFile(token.NewFileSet(), path, content, parser.AllErrors); err != nil {
						t.Fatalf("generated Go is invalid: %v", err)
					}
				}
				if strings.HasSuffix(path, ".yaml") {
					var document any
					if err := yaml.Unmarshal(content, &document); err != nil {
						t.Fatalf("generated YAML is invalid: %v", err)
					}
				}
			}
		})
	}
}

func TestGenerateRejectsInvalidFrameworkAndPackage(t *testing.T) {
	incident := t.TempDir()
	_ = os.WriteFile(filepath.Join(incident, "inbound.log"), nil, 0o600)
	if _, err := Generate(Options{IncidentDir: incident, OutputDir: t.TempDir(), Framework: "magic"}); err == nil {
		t.Fatal("expected framework error")
	}
	if _, err := Generate(Options{IncidentDir: incident, OutputDir: t.TempDir(), Package: "bad-package"}); err == nil {
		t.Fatal("expected package error")
	}
	if _, err := Generate(Options{IncidentDir: incident, OutputDir: t.TempDir(), Image: "image\nrun: injected"}); err == nil {
		t.Fatal("expected multiline image rejection")
	}
}

func TestGenerateRefusesOverwriteUnlessForced(t *testing.T) {
	incident := t.TempDir()
	_ = os.WriteFile(filepath.Join(incident, "inbound.log"), nil, 0o600)
	out := t.TempDir()
	options := Options{IncidentDir: incident, OutputDir: out, Framework: FrameworkDockerCompose}
	if _, err := Generate(options); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(options); err == nil {
		t.Fatal("expected overwrite refusal")
	}
	options.Force = true
	if _, err := Generate(options); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratePreflightsEveryOutputBeforeWriting(t *testing.T) {
	incident := t.TempDir()
	_ = os.WriteFile(filepath.Join(incident, "inbound.log"), nil, 0o600)
	out := t.TempDir()
	existing := filepath.Join(out, "infernosim_testcontainer_test.go")
	if err := os.WriteFile(existing, []byte("user-owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(Options{IncidentDir: incident, OutputDir: out, Framework: FrameworkGoTestcontainers}); err == nil {
		t.Fatal("expected overwrite refusal")
	}
	if _, err := os.Stat(filepath.Join(out, "README.md")); !os.IsNotExist(err) {
		t.Fatal("generation wrote a partial output before detecting a conflict")
	}
	content, _ := os.ReadFile(existing)
	if string(content) != "user-owned" {
		t.Fatal("generation modified an existing file")
	}
}

func TestGeneratedGoHarnessRejectsSymlinks(t *testing.T) {
	incident := t.TempDir()
	_ = os.WriteFile(filepath.Join(incident, "inbound.log"), nil, 0o600)
	out := t.TempDir()
	if _, err := Generate(Options{IncidentDir: incident, OutputDir: out, Framework: FrameworkGoTestcontainers}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(out, "infernosim_testcontainer_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "unsupported symlink") || !strings.Contains(string(content), "incident file escapes root") {
		t.Fatal("generated harness is missing filesystem boundary checks")
	}
}
