package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	QueriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dns_queries_total",
	}, []string{"qtype", "rcode"})

	QueryDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dns_query_duration_seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"qtype"})

	ZoneCacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "dns_zone_cache_hits_total",
	})

	ZoneCacheMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "dns_zone_cache_misses_total",
	})

	KeyDBCacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "dns_keydb_cache_hits_total",
	})

	KeyDBCacheMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "dns_keydb_cache_misses_total",
	})
)

func Start(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	return srv.ListenAndServe()
}
