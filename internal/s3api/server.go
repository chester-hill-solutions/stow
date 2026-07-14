package s3api

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

// Config configures the S3-compatible HTTP server.
type Config struct {
	Store       storage.Store
	Auth        AuthFunc
	Host        string // listen host, default 127.0.0.1
	Port        int    // 0 = ephemeral
	DataDir     string
	CORSOrigins []string
	Region      string
	// Mode is the operational mode reported by /_stow/status (local | run-through).
	Mode string
	// CachePolicy is the run-through cache policy (e.g. readThroughCache) or "none".
	CachePolicy string
	// WritePolicy is "local-only" or "allowLiveWrites".
	WritePolicy string
	// UpstreamHost is a redacted upstream endpoint host for status (no secrets).
	UpstreamHost string
}

// Server is the S3-compatible HTTP server.
type Server struct {
	config     Config
	store      storage.Store
	auth       AuthFunc
	httpServer *http.Server
	listener   net.Listener
	listenAddr string
	baseHost   string
	startTime  time.Time
}

// New creates a Server from config. Auth defaults to DevBypass if nil.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("s3api: Store is required")
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	authFn := cfg.Auth
	if authFn == nil {
		authFn = DevBypass
	}
	return &Server{
		config:    cfg,
		store:     cfg.Store,
		auth:      authFn,
		baseHost:  cfg.Host,
		startTime: time.Now(),
	}, nil
}

// ListenAndServe binds and serves HTTP. Blocks until the server stops.
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.listener = ln
	s.listenAddr = ln.Addr().String()
	// Update baseHost for virtual-hosted routing with actual bound port
	if host, port, err := net.SplitHostPort(s.listenAddr); err == nil {
		s.baseHost = host
		if s.config.Port == 0 {
			_ = port
		}
	}
	s.httpServer = &http.Server{Handler: s}
	return s.httpServer.Serve(ln)
}

// Addr returns the bound listen address (host:port).
func (s *Server) Addr() string {
	return s.listenAddr
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	reqID := newRequestID()
	ctx := withRequestID(r.Context(), reqID)
	r = r.WithContext(ctx)
	rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

	if handleCORSPreflight(rw, r) {
		s.logRequest(r, rw.status, time.Since(start))
		return
	}

	if isAdminPath(r.URL.Path) {
		s.handleAdmin(rw, r)
		s.logRequest(r, rw.status, time.Since(start))
		return
	}

	if s.auth != nil {
		if err := prepareRequestForAuth(r); err != nil {
			writeError(rw, r, s3Error{Code: "AccessDenied", Message: "cannot read request body", StatusCode: http.StatusForbidden})
			s.logRequest(r, rw.status, time.Since(start))
			return
		}
		if err := s.auth(r); err != nil {
			writeError(rw, r, authError(err))
			s.logRequest(r, rw.status, time.Since(start))
			return
		}
	}

	route, routeErr := parseRoute(r, s.baseHost)
	if routeErr.Code != "" {
		writeError(rw, r, routeErr)
		s.logRequest(r, rw.status, time.Since(start))
		return
	}

	s.dispatch(ctx, rw, r, route)
	s.logRequest(r, rw.status, time.Since(start))
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.written {
		r.status = code
		r.written = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.written {
		r.status = http.StatusOK
		r.written = true
	}
	return r.ResponseWriter.Write(b)
}

func (s *Server) logRequest(r *http.Request, status int, dur time.Duration) {
	log.Printf("%s %s %d %v", r.Method, r.URL.Path, status, dur)
}

// Handler returns an http.Handler for httptest.
func (s *Server) Handler() http.Handler {
	return s
}
