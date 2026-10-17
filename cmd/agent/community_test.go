package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"infernosim/pkg/agentreliability"
	"infernosim/pkg/agentrunner"
	"infernosim/pkg/causal"
)

func TestCausalRecordConvertExploreCLI(t *testing.T) {
	for _, contentType := range []string{"application/json", "application/x-ndjson"} {
		t.Run(contentType, func(t *testing.T) {
			expected := `{"ok":true}`
			if contentType == "application/x-ndjson" {
				expected = `{"model":"fixture","message":{"role":"assistant","content":"OK"},"done":false}` + "\n" + `{"model":"fixture","message":{"role":"assistant","content":""},"done":true,"eval_count":1}` + "\n"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", contentType)
				for _, frame := range strings.SplitAfter(expected, "\n") {
					fmt.Fprint(w, frame)
					http.NewResponseController(w).Flush()
				}
			}))
			defer server.Close()
			t.Setenv("INFERNOSIM_CLI_TEST_TARGET", server.URL)
			t.Setenv("INFERNOSIM_CLI_TEST_EXPECTED", expected)
			t.Setenv("GORACE", "atexit_sleep_ms=0")
			root := t.TempDir()
			incident, observed := filepath.Join(root, "incident"), filepath.Join(root, "observed.json")
			command := []string{os.Args[0], "-test.run=TestCommunityCLIApplicationHelper", "--", "request"}
			args := append([]string{incident, "--capture-sensitive-data", "--allow-private-destinations", "--timeout", "10s", "--"}, command...)
			if code := runCausalRecord(args); code != 0 {
				t.Fatal("record failed", code)
			}
			if code := runAgentCausal([]string{"convert", incident, "--review", "one awaited HTTP request", "--out", observed}); code != 0 {
				t.Fatal("conversion failed", code)
			}
			server.Close()
			args = append([]string{incident, "--observed-schedule", observed, "--report-dir", filepath.Join(root, "results"), "--timeout", "10s", "--"}, command...)
			if code := runAgentExplore(args); code != 0 {
				t.Fatal("offline observed replay failed", code)
			}
			if err := os.Remove(filepath.Join(incident, "capture-session.json")); err != nil {
				t.Fatal(err)
			}
			if code := runAgentCausal([]string{"convert", incident, "--review", "same request", "--out", filepath.Join(root, "partial.json")}); code != 1 {
				t.Fatal("unsealed capture became exact", code)
			}
		})
	}
}

func TestCommunityCLIApplicationHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "request" {
		return
	}
	proxy, _ := url.Parse(os.Getenv("INFERNOSIM_PROXY_URL"))
	transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
	defer transport.CloseIdleConnections()
	req, _ := http.NewRequest("POST", os.Getenv("INFERNOSIM_CLI_TEST_TARGET")+"/check", strings.NewReader(`{"item":"one"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(causal.IdentityHeader, "cli-request")
	response, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		os.Exit(3)
	}
	b, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || string(b) != os.Getenv("INFERNOSIM_CLI_TEST_EXPECTED") {
		os.Exit(4)
	}
	os.Exit(0)
}

func TestChangedCheckerCLICreatesInertNewCase(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "checker.py")
	if err := os.WriteFile(script, []byte("original checker"), 0600); err != nil {
		t.Fatal(err)
	}
	check := agentrunner.StateCheckOptions{Command: []string{os.Args[0], script}, IDs: []string{"destination"}, Timeout: time.Second}
	fingerprint, err := check.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(root, "original")
	m := agentrunner.Reproduction{Trial: agentrunner.Trial{Case: agentreliability.Case{ID: "baseline"}}, Checker: fingerprint, CheckIDs: check.IDs, CheckTimeout: time.Second, Timeout: time.Second, Assertion: "state:destination"}
	if err := agentrunner.ExportReproduction(artifact, "../../examples/agent-reliability", "", m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("changed checker"), 0600); err != nil {
		t.Fatal(err)
	}
	argv, _ := json.Marshal(check.Command)
	args := []string{artifact, "--check-command-json", string(argv), "--check-ids", "destination"}
	if code := runAgentReproduce(append(append([]string{}, args...), "--", "must-not-execute"), false); code != 1 {
		t.Fatal("changed checker accepted", code)
	}
	fork := filepath.Join(root, "fork")
	if code := runAgentReproduce(append(append([]string{}, args...), "--new-case-out", fork, "--check-timeout", "2s"), false); code != 0 {
		t.Fatal("explicit fork failed", code)
	}
	original, err := agentrunner.LoadReproduction(artifact)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := agentrunner.LoadReproduction(fork)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Version != 3 || changed.ParentCase != original.CaseIdentity || changed.CaseIdentity == original.CaseIdentity || changed.Assertion != "" || changed.CheckTimeout != 2*time.Second {
		t.Fatal("fork lineage/contract invalid", changed)
	}
}
