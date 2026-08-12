package testgen

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"infernosim/pkg/replaydriver"
)

const (
	FrameworkGoTestcontainers = "go-testcontainers"
	FrameworkDockerCompose    = "docker-compose"
	FrameworkGitHubActions    = "github-actions"
)

type Options struct {
	IncidentDir string
	OutputDir   string
	Framework   string
	Image       string
	Package     string
	Force       bool
}

type Result struct {
	Files []string
}

type templateData struct {
	IncidentPath           string
	RepositoryIncidentPath string
	Image                  string
	Package                string
}

func Generate(opts Options) (Result, error) {
	if _, err := replaydriver.OpenBundle(opts.IncidentDir); err != nil {
		return Result{}, err
	}
	if opts.OutputDir == "" {
		return Result{}, fmt.Errorf("output directory is required")
	}
	if opts.Framework == "" {
		opts.Framework = FrameworkGoTestcontainers
	}
	if opts.Image == "" {
		opts.Image = "ghcr.io/pranaysparihar/infernosim:3.4.0"
	}
	if strings.ContainsAny(opts.Image, "\r\n") || strings.TrimSpace(opts.Image) == "" {
		return Result{}, fmt.Errorf("image must be a non-empty single-line reference")
	}
	if opts.Package == "" {
		opts.Package = "integration"
	}
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(opts.Package) {
		return Result{}, fmt.Errorf("package must be a valid Go identifier")
	}
	absIncident, err := filepath.Abs(opts.IncidentDir)
	if err != nil {
		return Result{}, err
	}
	absOutput, err := filepath.Abs(opts.OutputDir)
	if err != nil {
		return Result{}, err
	}
	relativeIncident := absIncident
	repositoryIncident := ""
	if opts.Framework == FrameworkGitHubActions {
		workingDirectory, err := os.Getwd()
		if err != nil {
			return Result{}, err
		}
		repositoryIncident, err = filepath.Rel(workingDirectory, absIncident)
		if err != nil || repositoryIncident == ".." || strings.HasPrefix(repositoryIncident, ".."+string(filepath.Separator)) || filepath.IsAbs(repositoryIncident) {
			return Result{}, fmt.Errorf("GitHub Actions generation requires the incident to be inside the current repository")
		}
	} else if candidate, relErr := filepath.Rel(absOutput, absIncident); relErr == nil {
		// A portable relative path is preferable, but Windows cannot construct one
		// when the output and incident are on different volumes. An absolute host
		// path remains valid for the local Testcontainers and Compose harnesses.
		relativeIncident = candidate
	}
	if strings.ContainsAny(relativeIncident, "\r\n") || strings.ContainsAny(repositoryIncident, "\r\n") {
		return Result{}, fmt.Errorf("incident path cannot contain a newline")
	}
	data := templateData{
		IncidentPath: filepath.ToSlash(relativeIncident), RepositoryIncidentPath: filepath.ToSlash(repositoryIncident),
		Image: opts.Image, Package: opts.Package,
	}
	var files map[string]string
	switch opts.Framework {
	case FrameworkGoTestcontainers:
		files = map[string]string{
			"infernosim_testcontainer_test.go": goTestcontainersTemplate,
			"README.md":                        goReadmeTemplate,
		}
	case FrameworkDockerCompose:
		files = map[string]string{"compose.infernosim.yaml": composeTemplate}
	case FrameworkGitHubActions:
		files = map[string]string{"infernosim-ci.yaml": githubActionsTemplate}
	default:
		return Result{}, fmt.Errorf("unsupported framework %q (expected %s, %s, or %s)", opts.Framework, FrameworkGoTestcontainers, FrameworkDockerCompose, FrameworkGitHubActions)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	renderedFiles := make(map[string][]byte, len(files))
	for _, name := range names {
		source := files[name]
		var rendered bytes.Buffer
		tmpl, err := template.New(name).Funcs(template.FuncMap{
			"yamlQuote": func(value string) string {
				return "'" + strings.ReplaceAll(value, "'", "''") + "'"
			},
		}).Option("missingkey=error").Parse(source)
		if err != nil {
			return Result{}, err
		}
		if err := tmpl.Execute(&rendered, data); err != nil {
			return Result{}, err
		}
		content := rendered.Bytes()
		if strings.HasSuffix(name, ".go") {
			content, err = format.Source(content)
			if err != nil {
				return Result{}, fmt.Errorf("format generated %s: %w", name, err)
			}
		}
		renderedFiles[name] = content
	}
	if err := os.MkdirAll(opts.OutputDir, 0o700); err != nil {
		return Result{}, err
	}
	if !opts.Force {
		for _, name := range names {
			path := filepath.Join(opts.OutputDir, name)
			if _, err := os.Stat(path); err == nil {
				return Result{}, fmt.Errorf("refusing to overwrite %s without force", path)
			} else if !os.IsNotExist(err) {
				return Result{}, err
			}
		}
	}
	var result Result
	for _, name := range names {
		path := filepath.Join(opts.OutputDir, name)
		if err := writeAtomic(path, renderedFiles[name]); err != nil {
			return result, err
		}
		result.Files = append(result.Files, path)
	}
	return result, nil
}

func writeAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".infernosim-testgen-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}

const goTestcontainersTemplate = `// Code generated by InfernoSIM; safe to edit.
package {{.Package}}

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const infernoSIMIncident = {{printf "%q" .IncidentPath}}
const infernoSIMImage = {{printf "%q" .Image}}

type infernoSIMContainer struct {
	testcontainers.Container
	ProxyURL string
	AdminURL string
}

func startInfernoSIM(t testing.TB) *infernoSIMContainer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	files := make([]testcontainers.ContainerFile, 0)
	err := filepath.WalkDir(infernoSIMIncident, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("incident contains unsupported symlink %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(infernoSIMIncident, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("incident file escapes root: %s", path)
		}
		files = append(files, testcontainers.ContainerFile{
			HostFilePath: path,
			ContainerFilePath: "/incident/" + filepath.ToSlash(relative),
			FileMode: 0o444,
		})
		return nil
	})
	if err != nil {
		t.Fatalf("collect InfernoSIM incident: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("InfernoSIM incident contains no files")
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: infernoSIMImage,
			ExposedPorts: []string{"19000/tcp", "19001/tcp"},
			Files: files,
			Cmd: []string{"serve", "/incident", "--listen", "0.0.0.0:19000", "--admin-listen", "0.0.0.0:19001"},
			WaitingFor: wait.ForHTTP("/healthz").WithPort("19001/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start InfernoSIM: %v", err)
	}
	t.Cleanup(func() {
		terminateContext, terminateCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer terminateCancel()
		if err := container.Terminate(terminateContext); err != nil {
			t.Errorf("terminate InfernoSIM: %v", err)
		}
	})
	proxyURL, err := container.PortEndpoint(ctx, "19000/tcp", "http")
	if err != nil {
		t.Fatalf("resolve InfernoSIM proxy: %v", err)
	}
	adminURL, err := container.PortEndpoint(ctx, "19001/tcp", "http")
	if err != nil {
		t.Fatalf("resolve InfernoSIM admin API: %v", err)
	}
	return &infernoSIMContainer{Container: container, ProxyURL: proxyURL, AdminURL: adminURL}
}

func (container *infernoSIMContainer) reset(t testing.TB) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, container.AdminURL+"/__infernosim/reset", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("reset InfernoSIM: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reset InfernoSIM: %s", response.Status)
	}
}

func TestInfernoSIMIncidentLoads(t *testing.T) {
	if _, err := os.Stat(infernoSIMIncident); err != nil {
		t.Fatalf("incident is unavailable: %v", err)
	}
	simulator := startInfernoSIM(t)
	simulator.reset(t)
	if simulator.ProxyURL == "" {
		t.Fatal(fmt.Errorf("InfernoSIM proxy URL is empty"))
	}
	// Configure your application under test with HTTP_PROXY and HTTPS_PROXY set
	// to simulator.ProxyURL, exercise the incident, and assert its response.
}
`

const goReadmeTemplate = `# Generated InfernoSIM integration

This directory contains a readable Testcontainers-Go harness for the incident at:

    {{.IncidentPath}}

Add the dependency and run the generated smoke test:

    go get github.com/testcontainers/testcontainers-go@v0.44.0
    go test ./...

Set your application under test's HTTP_PROXY and HTTPS_PROXY to the returned
ProxyURL. Call reset before each scenario. The control API is deliberately
bound to a separate port and never exposes captured payloads.
`

const composeTemplate = `services:
  infernosim:
    image: {{yamlQuote .Image}}
    command: ["serve", "/incident", "--listen", "0.0.0.0:19000", "--admin-listen", "0.0.0.0:19001"]
    volumes:
      - type: bind
        source: {{yamlQuote .IncidentPath}}
        target: /incident
        read_only: true
    ports:
      - "19000:19000"
      - "19001:19001"
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:19001/healthz"]
      interval: 2s
      timeout: 1s
      retries: 30
    read_only: true
    tmpfs:
      - /tmp
`

const githubActionsTemplate = `name: InfernoSIM incident test

on:
  pull_request:
  push:

jobs:
  incident-test:
    runs-on: ubuntu-latest
    env:
      INFERNOSIM_INCIDENT_DIR: {{yamlQuote .RepositoryIncidentPath}}
      INFERNOSIM_IMAGE: {{yamlQuote .Image}}
    steps:
      - uses: actions/checkout@v4
      - name: Start InfernoSIM
        run: |
          docker run --detach --rm --name infernosim \
            --publish 19000:19000 --publish 19001:19001 \
            --volume "$PWD/$INFERNOSIM_INCIDENT_DIR:/incident:ro" \
            "$INFERNOSIM_IMAGE" serve /incident --listen 0.0.0.0:19000 --admin-listen 0.0.0.0:19001
          for _ in $(seq 1 30); do
            curl --fail --silent http://127.0.0.1:19001/healthz && exit 0
            sleep 1
          done
          docker logs infernosim
          exit 1
      - name: Run application incident tests
        env:
          HTTP_PROXY: http://127.0.0.1:19000
          HTTPS_PROXY: http://127.0.0.1:19000
        run: go test ./...
      - name: Save deterministic proof
        if: always()
        run: curl --fail --silent http://127.0.0.1:19001/__infernosim/proof > infernosim-proof.json
      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: infernosim-proof
          path: infernosim-proof.json
`
