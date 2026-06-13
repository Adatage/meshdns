package dnsserver

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/miekg/dns"

	"github.com/Adatage/meshdns/internal/config"
	"github.com/Adatage/meshdns/internal/keydb"
)

type Server struct {
	cfg     *config.Config
	handler dns.Handler
	log     *slog.Logger

	mu      sync.Mutex
	started []*dns.Server
}

func New(cfg *config.Config, auth *Authoritative, cache *keydb.Client, log *slog.Logger) *Server {
	handler := NewHandler(cfg, auth, cache, log)
	return &Server{
		cfg:     cfg,
		handler: handler,
		log:     log,
	}
}

func (s *Server) Start(ctx context.Context) error {
	if !s.cfg.UDPEnabled && !s.cfg.TCPEnabled {
		return fmt.Errorf("both UDP and TCP are disabled; nothing to start")
	}

	var planned []*dns.Server

	if s.cfg.UDPEnabled {
		srv := &dns.Server{
			Addr:    s.cfg.UDPAddr(),
			Net:     "udp",
			Handler: s.handler,
		}
		planned = append(planned, srv)
	}

	if s.cfg.TCPEnabled {
		srv := &dns.Server{
			Addr:    s.cfg.TCPAddr(),
			Net:     "tcp",
			Handler: s.handler,
		}
		planned = append(planned, srv)
	}

	errCh := make(chan error, len(planned))
	var wg sync.WaitGroup

	for _, srv := range planned {
		wg.Add(1)
		srv := srv

		srv.NotifyStartedFunc = func() {
			s.mu.Lock()
			s.started = append(s.started, srv)
			s.mu.Unlock()
			s.log.Info("DNS listener ready", "net", srv.Net, "addr", srv.Addr)
		}
		s.log.Info("DNS listener starting", "net", srv.Net, "addr", srv.Addr)
		go func(srv *dns.Server) {
			defer wg.Done()
			if err := srv.ListenAndServe(); err != nil {
				errCh <- fmt.Errorf("DNS %s listener: %w", srv.Net, err)
			}
		}(srv)
	}

	select {
	case <-ctx.Done():
		s.Shutdown()
		wg.Wait()
		return nil
	case err := <-errCh:
		s.Shutdown()
		wg.Wait()
		return err
	}
}

func (s *Server) Shutdown() {
	s.mu.Lock()
	toStop := make([]*dns.Server, len(s.started))
	copy(toStop, s.started)
	s.mu.Unlock()

	for _, srv := range toStop {
		if err := srv.Shutdown(); err != nil {
			s.log.Warn("DNS server shutdown error", "net", srv.Net, "err", err)
		}
	}
}
