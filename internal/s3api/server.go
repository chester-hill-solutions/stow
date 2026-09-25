package s3api

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/chester-hill-solutions/stow/internal/storage"
)

// Config configures the S3-compatible HTTP server.
type Config struct {
	Store       storage.Store
	Auth        AuthFunc
	Host        string // listen host, default 127.0.0.1
	Port        int    // 0 = ephemeral
	BaseHost    string // host suffix for virtual-hosted-style routing
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
	// AllowPublicAdmin explicitly permits admin and metrics routes on non-loopback requests.
	AllowPublicAdmin bool
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
	mu         sync.RWMutex
	ready      chan struct{}
	readyOnce  sync.Once
	started    bool
	closed     bool
	closeOnce  sync.Once
	closeErr   error
	startTime  time.Time
}

// New creates a Server from config. Authentication must be explicit.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("s3api: Store is required")
	}
	if cfg.Auth == nil {
		return nil, fmt.Errorf("s3api: Auth is required")
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	baseHost := cfg.BaseHost
	if baseHost == "" {
		baseHost = hostWithoutPort(cfg.Host)
	}
	authFn := cfg.Auth
	return &Server{
		config:    cfg,
		store:     cfg.Store,
		auth:      authFn,
		baseHost:  baseHost,
		ready:     make(chan struct{}),
		startTime: time.Now(),
	}, nil
}

// ListenAndServe binds and serves HTTP. Blocks until the server stops.
func (s *Server) ListenAndServe() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.readyOnce.Do(func() { close(s.ready) })
		return http.ErrServerClosed
	}
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("s3api: server is already started")
	}
	s.started = true
	ready := s.ready
	s.mu.Unlock()

	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.readyOnce.Do(func() { close(ready) })
		return err
	}
	listenAddr := ln.Addr().String()
	s.mu.RLock()
	baseHost := s.baseHost
	s.mu.RUnlock()
	// Use an explicitly configured base host for virtual-hosted routing. Otherwise
	// derive the suffix from the actual bound address.
	if s.config.BaseHost == "" {
		if host, _, err := net.SplitHostPort(listenAddr); err == nil {
			baseHost = host
		}
	}
	httpServer := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	s.mu.Lock()
	s.listener = ln
	s.listenAddr = listenAddr
	s.baseHost = baseHost
	s.httpServer = httpServer
	s.mu.Unlock()
	s.readyOnce.Do(func() { close(ready) })
	return httpServer.Serve(ln)
}

// Addr returns the bound listen address (host:port).
func (s *Server) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listenAddr
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	started := s.started
	ready := s.ready
	httpServer := s.httpServer
	s.mu.Unlock()

	var shutdownErr error
	if started && httpServer == nil {
		select {
		case <-ready:
			s.mu.RLock()
			httpServer = s.httpServer
			s.mu.RUnlock()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if httpServer != nil {
		shutdownErr = httpServer.Shutdown(ctx)
	}
	s.closeOnce.Do(func() { s.closeErr = s.store.Close() })
	if shutdownErr != nil {
		return shutdownErr
	}
	return s.closeErr
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
		if !s.config.AllowPublicAdmin && !isLoopbackRequest(r) {
			http.NotFound(rw, r)
			s.logRequest(r, rw.status, time.Since(start))
			return
		}
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

	s.mu.RLock()
	baseHost := s.baseHost
	s.mu.RUnlock()
	route, routeErr := parseRoute(r, baseHost)
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
	path := r.URL.Path
	if !isAdminPath(path) {
		path = "/s3"
	}
	log.Printf("%s %s %d %v", r.Method, path, status, dur)
}

func isLoopbackRequest(r *http.Request) bool {
	remote := r.RemoteAddr
	if remote == "" {
		return false
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Handler returns an http.Handler for httptest.
func (s *Server) Handler() http.Handler {
	return s
}
