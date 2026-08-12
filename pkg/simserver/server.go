package simserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"infernosim/pkg/capture"
	"infernosim/pkg/replaydriver"
	"infernosim/pkg/stubproxy"
)

const controlPrefix = "/__infernosim"

type Options struct {
	IncidentDir string
	ConfigPath  string
	Listen      string
	AdminListen string
	ObservedLog string
	HTTPS       bool
	CADir       string
	AllowHosts  []string
}

type Server struct {
	options       Options
	stub          *stubproxy.StubProxy
	stubServer    *http.Server
	adminServer   *http.Server
	stubListener  net.Listener
	adminListener net.Listener
	proof         Proof
	errors        chan error
	closeOnce     sync.Once
	closeErr      error
}

type Proof struct {
	Version      int                `json:"version"`
	IncidentHash string             `json:"incident_hash"`
	ConfigHash   string             `json:"config_hash,omitempty"`
	SemanticHash string             `json:"semantic_hash"`
	StartedAt    time.Time          `json:"started_at"`
	Snapshot     stubproxy.Snapshot `json:"snapshot"`
}

func New(opts Options) (*Server, error) {
	if strings.TrimSpace(opts.IncidentDir) == "" {
		return nil, fmt.Errorf("incident directory is required")
	}
	if opts.Listen == "" {
		opts.Listen = "127.0.0.1:19000"
	}
	if opts.AdminListen == "" {
		opts.AdminListen = "127.0.0.1:19001"
	}
	bundle, err := replaydriver.OpenBundle(opts.IncidentDir)
	if err != nil {
		return nil, err
	}
	configPath := opts.ConfigPath
	if configPath == "" && bundle.HasConfig() {
		configPath = bundle.ConfigPath
	}
	var config replaydriver.ReplayYAMLConfig
	if configPath != "" {
		config, err = replaydriver.LoadReplayConfig(configPath)
		if err != nil {
			return nil, err
		}
	}
	if config.Stub.HTTPS.Enabled {
		opts.HTTPS = true
	}
	if opts.CADir == "" {
		opts.CADir = config.Stub.HTTPS.CADir
	}
	if len(opts.AllowHosts) == 0 {
		opts.AllowHosts = append([]string(nil), config.Stub.HTTPS.AllowHosts...)
	}
	var ca *capture.CAStore
	if opts.HTTPS {
		if opts.CADir == "" {
			ca, err = capture.NewCAStore()
		} else {
			ca, err = capture.NewCAStoreAt(opts.CADir)
		}
		if err != nil {
			return nil, fmt.Errorf("initialize HTTPS stub CA: %w", err)
		}
		if len(opts.AllowHosts) > 0 {
			ca.AllowedHosts = append([]string(nil), opts.AllowHosts...)
		}
	}
	stub, err := stubproxy.NewWithOptions(bundle.OutboundLog, opts.ObservedLog, nil, stubproxy.Options{
		Matching:  config.Matching,
		Scenarios: config.Scenarios,
		Templates: config.Templates,
		TLSCA:     ca,
	})
	if err != nil {
		return nil, err
	}
	incidentHash, err := hashFiles(bundle.MetadataPath, bundle.InboundLog, bundle.OutboundLog, filepath.Join(bundle.Dir, "messages.log"))
	if err != nil {
		_ = stub.Close()
		return nil, err
	}
	configHash := ""
	if configPath != "" {
		configHash, err = hashFiles(configPath)
		if err != nil {
			_ = stub.Close()
			return nil, err
		}
	}
	s := &Server{
		options: opts,
		stub:    stub,
		errors:  make(chan error, 2),
		proof: Proof{
			Version:      1,
			IncidentHash: incidentHash,
			ConfigHash:   configHash,
			StartedAt:    time.Now().UTC(),
		},
	}
	s.stubServer = &http.Server{Handler: stub.Handler(), ReadHeaderTimeout: 10 * time.Second}
	s.adminServer = &http.Server{Handler: s.controlHandler(), ReadHeaderTimeout: 5 * time.Second}
	return s, nil
}

func (s *Server) Start() error {
	stubListener, err := net.Listen("tcp", s.options.Listen)
	if err != nil {
		return fmt.Errorf("listen on simulator address %s: %w", s.options.Listen, err)
	}
	adminListener, err := net.Listen("tcp", s.options.AdminListen)
	if err != nil {
		_ = stubListener.Close()
		return fmt.Errorf("listen on admin address %s: %w", s.options.AdminListen, err)
	}
	s.stubListener = stubListener
	s.adminListener = adminListener
	go serve(s.stubServer, stubListener, s.errors)
	go serve(s.adminServer, adminListener, s.errors)
	return nil
}

func serve(server *http.Server, listener net.Listener, errCh chan<- error) {
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		_ = listener.Close()
		errCh <- err
	}
}

// Errors reports an unexpected listener failure. Normal shutdown does not
// publish an error.
func (s *Server) Errors() <-chan error { return s.errors }

func (s *Server) StubAddress() string {
	if s.stubListener == nil {
		return ""
	}
	return s.stubListener.Addr().String()
}

func (s *Server) AdminAddress() string {
	if s.adminListener == nil {
		return ""
	}
	return s.adminListener.Addr().String()
}

func (s *Server) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		var errs []error
		if s.adminServer != nil {
			errs = append(errs, s.adminServer.Shutdown(ctx))
		}
		if s.stubServer != nil {
			errs = append(errs, s.stubServer.Shutdown(ctx))
		}
		if s.stub != nil {
			errs = append(errs, s.stub.Close())
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

func (s *Server) controlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET "+controlPrefix+"/healthz", s.handleHealth)
	mux.HandleFunc("GET "+controlPrefix+"/status", s.handleStatus)
	mux.HandleFunc("POST "+controlPrefix+"/reset", s.handleReset)
	mux.HandleFunc("GET "+controlPrefix+"/proof", s.handleProof)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.stub.Snapshot())
}

func (s *Server) handleReset(w http.ResponseWriter, _ *http.Request) {
	s.stub.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"status": "reset"})
}

func (s *Server) handleProof(w http.ResponseWriter, _ *http.Request) {
	proof := s.proof
	proof.Snapshot = s.stub.Snapshot()
	semantic, _ := json.Marshal(struct {
		Version      int
		IncidentHash string
		ConfigHash   string
		Snapshot     stubproxy.Snapshot
	}{proof.Version, proof.IncidentHash, proof.ConfigHash, proof.Snapshot})
	proof.SemanticHash = hashBytes(semantic)
	writeJSON(w, http.StatusOK, proof)
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func hashFiles(paths ...string) (string, error) {
	hash := sha256.New()
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("hash %s: %w", path, err)
		}
		_, _ = hash.Write([]byte(filepath.Base(path)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
