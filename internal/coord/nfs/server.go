package nfs

import (
	"crypto/tls"
	"net"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
	nfs "github.com/willscott/go-nfs"
)

// Server provides an NFS v3 server backed by S3 storage.
type Server struct {
	handler   *Handler
	listener  net.Listener
	tlsConfig *tls.Config
	addr      string
}

// Config holds NFS server configuration.
type Config struct {
	// Address to bind to (e.g., ":2049" or "10.100.0.1:2049")
	Address string

	// TLS configuration for authentication
	TLSConfig *tls.Config
}

// NewServer creates a new NFS server.
func NewServer(
	store *s3.Store,
	shares *s3.FileShareManager,
	authorizer *auth.Authorizer,
	passwords *PasswordStore,
	cfg Config,
) *Server {
	handler := NewHandler(store, shares, authorizer, passwords)

	return &Server{
		handler:   handler,
		tlsConfig: cfg.TLSConfig,
		addr:      cfg.Address,
	}
}

// Start starts the NFS server.
func (s *Server) Start() error {
	var listener net.Listener
	var err error

	if s.tlsConfig != nil {
		listener, err = tls.Listen("tcp", s.addr, s.tlsConfig)
	} else {
		listener, err = net.Listen("tcp", s.addr)
	}
	if err != nil {
		return err
	}

	s.listener = listener

	log.Info().
		Str("addr", s.addr).
		Bool("tls", s.tlsConfig != nil).
		Msg("NFS server started")

	// Run in goroutine
	go func() {
		if err := nfs.Serve(listener, s.handler); err != nil {
			log.Error().Err(err).Msg("NFS server error")
		}
	}()

	return nil
}

// Stop stops the NFS server.
func (s *Server) Stop() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// Handler returns the NFS handler for testing.
func (s *Server) Handler() *Handler {
	return s.handler
}
