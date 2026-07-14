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
	Host        string   // listen host, default 127.0.0.1
	Port        int      // 0 = ephemeral
	DataDir     string
	CORSOrigins []string
	Region      string
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

	if handleCORSPreflight(w, r) {
		s.logRequest(r, http.StatusOK, time.Since(start))
		return
	}

	if isAdminPath(r.URL.Path) {
		s.handleAdmin(w, r)
		s.logRequest(r, 200, time.Since(start))
		return
	}

	if s.auth != nil {
		if err := prepareRequestForAuth(r); err != nil {
			writeError(w, r, s3Error{Code: "AccessDenied", Message: "cannot read request body", StatusCode: http.StatusForbidden})
			s.logRequest(r, http.StatusForbidden, time.Since(start))
			return
		}
		if err := s.auth(r); err != nil {
			writeError(w, r, authError(err))
			s.logRequest(r, http.StatusForbidden, time.Since(start))
			return
		}
	}

	route, routeErr := parseRoute(r, s.baseHost)
	if routeErr.Code != "" {
		writeError(w, r, routeErr)
		s.logRequest(r, routeErr.StatusCode, time.Since(start))
		return
	}

	s.dispatch(ctx, w, r, route)
	s.logRequest(r, 0, time.Since(start))
}

func (s *Server) logRequest(r *http.Request, status int, dur time.Duration) {
	if status == 0 {
		// status unknown from response writer wrapper; log without it
		log.Printf("%s %s %v", r.Method, r.URL.Path, dur)
		return
	}
	log.Printf("%s %s %d %v", r.Method, r.URL.Path, status, dur)
}

// Handler returns an http.Handler for httptest.
func (s *Server) Handler() http.Handler {
	return s
}
