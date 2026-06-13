package dnsserver

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/Adatage/meshdns/internal/config"
	"github.com/Adatage/meshdns/internal/keydb"
	"github.com/Adatage/meshdns/internal/metrics"
)

type Handler struct {
	cfg      *config.Config
	auth     *Authoritative
	cache    *keydb.Client
	resolver *Resolver
	log      *slog.Logger
}

func NewHandler(
	cfg *config.Config,
	auth *Authoritative,
	cache *keydb.Client,
	log *slog.Logger,
) *Handler {
	h := &Handler{
		cfg:   cfg,
		auth:  auth,
		cache: cache,
		log:   log,
	}
	if cfg.RecursiveEnabled {
		h.resolver = NewResolver()
	}
	return h
}

func (h *Handler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = false
	m.RecursionAvailable = h.cfg.RecursiveEnabled

	if len(r.Question) == 0 {
		m.SetRcode(r, dns.RcodeFormatError)
		_ = w.WriteMsg(m)
		return
	}

	q := r.Question[0]
	qtype := dns.TypeToString[q.Qtype]
	ctx := context.Background()

	var clientIP net.IP
	if host, _, err := net.SplitHostPort(w.RemoteAddr().String()); err == nil {
		clientIP = net.ParseIP(host)
	}

	start := time.Now()
	defer func() {
		rcode := dns.RcodeToString[m.Rcode]
		metrics.QueriesTotal.WithLabelValues(qtype, rcode).Inc()
		metrics.QueryDurationSeconds.WithLabelValues(qtype).Observe(time.Since(start).Seconds())
	}()

	h.log.Debug("query",
		"name", q.Name,
		"type", qtype,
		"from", w.RemoteAddr().String(),
	)

	if h.auth != nil {
		answered, err := h.auth.Answer(ctx, m, q, clientIP)
		if err != nil {
			h.log.Error("authoritative lookup error", "err", err)
			m.SetRcode(r, dns.RcodeServerFailure)
			_ = w.WriteMsg(m)
			return
		}
		if answered {
			_ = w.WriteMsg(m)
			return
		}
	}

	if h.cache != nil {
		rrs, rcode, hit, err := getCacheEntry(ctx, h.cache, q.Name, q.Qtype)
		if err != nil {
			h.log.Warn("cache get error", "err", err)
		} else if hit {
			m.Answer = rrs
			m.Rcode = rcode
			_ = w.WriteMsg(m)
			return
		}
	}

	if h.resolver != nil {
		resp, err := h.resolver.Resolve(strings.ToLower(q.Name), q.Qtype)
		if err != nil {
			h.log.Warn("recursive resolution failed", "name", q.Name, "err", err)
			m.SetRcode(r, dns.RcodeServerFailure)
			_ = w.WriteMsg(m)
			return
		}
		m.Answer = resp.Answer
		m.Ns = resp.Ns
		m.Extra = resp.Extra
		m.Rcode = resp.Rcode

		if h.cache != nil {
			if resp.Rcode == dns.RcodeSuccess && len(resp.Answer) > 0 {
				if err := setCacheEntry(ctx, h.cache, q.Name, q.Qtype, resp.Answer, resp.Rcode, 0); err != nil {
					h.log.Warn("cache set error", "err", err)
				}
			} else if resp.Rcode == dns.RcodeNameError {
				if err := setCacheEntry(ctx, h.cache, q.Name, q.Qtype, nil, resp.Rcode, h.cfg.NegativeTTL); err != nil {
					h.log.Warn("negative cache set error", "err", err)
				}
			}
		}
		_ = w.WriteMsg(m)
		return
	}

	m.SetRcode(r, dns.RcodeRefused)
	_ = w.WriteMsg(m)
}

