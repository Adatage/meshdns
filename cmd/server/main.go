package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	dnsserver "github.com/Adatage/meshdns/internal/dns"
	grpcserver "github.com/Adatage/meshdns/internal/grpc"

	"github.com/Adatage/meshdns/internal/cockroach"
	"github.com/Adatage/meshdns/internal/config"
	"github.com/Adatage/meshdns/internal/keydb"
	"github.com/Adatage/meshdns/internal/metrics"
	"github.com/Adatage/meshdns/internal/zonecache"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration error", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var kdb *keydb.Client
	if cfg.CacheEnabled() {
		kdb, err = keydb.New(cfg.KeyDBAddr, cfg.KeyDBPassword, cfg.KeyDBDB)
		if err != nil {
			log.Error("keydb connection failed", "addr", cfg.KeyDBAddr, "err", err)
			os.Exit(1)
		}
		defer kdb.Close()
		log.Info("keydb cache connected", "addr", cfg.KeyDBAddr)
	} else {
		log.Info("keydb cache disabled (KEYDB_ADDR not set)")
	}

	var db *cockroach.DB
	if cfg.AuthoritativeEnabled() {
		db, err = cockroach.New(ctx, cfg.CockroachDSN)
		if err != nil {
			log.Error("cockroachdb connection failed", "err", err)
			os.Exit(1)
		}
		defer db.Close()
		log.Info("cockroachdb authoritative store connected")
	} else {
		log.Info("authoritative mode disabled (COCKROACH_DSN not set)")
	}

	if cfg.RecursiveEnabled {
		log.Info("recursive resolver enabled")
	} else {
		log.Info("recursive resolver disabled (DNS_RECURSIVE_ENABLED not set)")
	}

	zc := zonecache.New()

	var auth *dnsserver.Authoritative
	if db != nil {
		auth = dnsserver.NewAuthoritative(db, zc, cfg.NegativeTTL)
	}

	dnsSrv := dnsserver.New(cfg, auth, kdb, log)
	grpcSrv := grpcserver.New(cfg, db, zc, log)

	var wg sync.WaitGroup
	errCh := make(chan error, 3)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := dnsSrv.Start(ctx); err != nil {
			errCh <- err
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := grpcSrv.Start(ctx); err != nil {
			errCh <- err
		}
	}()

	if cfg.MetricsAddr != "" {
		log.Info("metrics server starting", "addr", cfg.MetricsAddr)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := metrics.Start(ctx, cfg.MetricsAddr); err != nil && err != http.ErrServerClosed {
				errCh <- err
			}
		}()
	}

	select {
	case err := <-errCh:
		log.Error("fatal server error", "err", err)
		stop()
	case <-ctx.Done():
		log.Info("shutting down")
	}

	wg.Wait()
}

