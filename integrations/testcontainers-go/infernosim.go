// Package infernosim provides a small Testcontainers-Go adapter for running a
// sanitized InfernoSIM incident as a dependency simulator in tests.
package infernosim

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const DefaultImage = "ghcr.io/pranaysparihar/infernosim:3.4.0"

type Options struct {
	Image          string
	IncidentDir    string
	StartupTimeout time.Duration
	HTTPS          bool
	AllowHosts     []string
}

type Container struct {
	testcontainers.Container
	ProxyURL string
	AdminURL string
}

func Run(ctx context.Context, options Options) (*Container, error) {
	if options.Image == "" {
		options.Image = DefaultImage
	}
	if options.IncidentDir == "" {
		return nil, fmt.Errorf("incident directory is required")
	}
	if options.StartupTimeout == 0 {
		options.StartupTimeout = 60 * time.Second
	}
	files, err := incidentFiles(options.IncidentDir)
	if err != nil {
		return nil, err
	}
	command := []string{"serve", "/incident", "--listen", "0.0.0.0:19000", "--admin-listen", "0.0.0.0:19001"}
	if options.HTTPS {
		command = append(command, "--https-stub")
	}
	if len(options.AllowHosts) > 0 {
		command = append(command, "--stub-mitm-allow-hosts", strings.Join(options.AllowHosts, ","))
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        options.Image,
			ExposedPorts: []string{"19000/tcp", "19001/tcp"},
			Files:        files,
			Cmd:          command,
			WaitingFor: wait.ForHTTP("/healthz").
				WithPort("19001/tcp").
				WithStartupTimeout(options.StartupTimeout),
		},
		Started: true,
	})
	if err != nil {
		return nil, err
	}
	cleanup := func(original error) (*Container, error) {
		terminateContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = container.Terminate(terminateContext)
		return nil, original
	}
	proxyURL, err := container.PortEndpoint(ctx, "19000/tcp", "http")
	if err != nil {
		return cleanup(err)
	}
	adminURL, err := container.PortEndpoint(ctx, "19001/tcp", "http")
	if err != nil {
		return cleanup(err)
	}
	return &Container{Container: container, ProxyURL: proxyURL, AdminURL: adminURL}, nil
}

func (container *Container) Reset(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, container.AdminURL+"/__infernosim/reset", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("InfernoSIM reset returned %s", response.Status)
	}
	return nil
}

func incidentFiles(root string) ([]testcontainers.ContainerFile, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := filepath.EvalSymlinks(absRoot); err != nil {
		return nil, fmt.Errorf("resolve incident directory: %w", err)
	}
	var files []testcontainers.ContainerFile
	err = filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("incident contains unsupported symlink %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(absRoot, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("incident file escapes root: %s", path)
		}
		files = append(files, testcontainers.ContainerFile{
			HostFilePath: path, ContainerFilePath: "/incident/" + filepath.ToSlash(relative), FileMode: 0o444,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("incident directory contains no files")
	}
	return files, nil
}
