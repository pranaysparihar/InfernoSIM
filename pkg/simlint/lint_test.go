package simlint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLintFindsUnreachableAndShadowedSteps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.yaml")
	config := `scenarios:
  - name: checkout
    initial_state: new
    steps:
      - name: start
        state: new
        match:
          methods: [POST]
          path_regex: "^/start$"
        response:
          status: 200
      - name: duplicate
        state: new
        match:
          methods: [POST]
          path_regex: "^/start$"
        response:
          status: 200
      - name: orphan
        state: unreachable
        match:
          methods: [GET]
          path_regex: "^/orphan$"
        response:
          status: 200
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := File(path)
	if err != nil {
		t.Fatal(err)
	}
	codes := make(map[string]bool)
	for _, diagnostic := range result.Diagnostics {
		codes[diagnostic.Code] = true
	}
	if !codes["STEP_SHADOWED"] || !codes["STATE_UNREACHABLE"] {
		t.Fatalf("diagnostics=%#v", result.Diagnostics)
	}
}

func TestLintReportsStrictConfigErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.yaml")
	if err := os.WriteFile(path, []byte("unknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := File(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCount() != 1 || result.Diagnostics[0].Code != "CONFIG_INVALID" {
		t.Fatalf("diagnostics=%#v", result.Diagnostics)
	}
}
