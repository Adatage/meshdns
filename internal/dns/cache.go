package dnsserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/Adatage/meshdns/internal/keydb"
	"github.com/Adatage/meshdns/internal/metrics"
)

const cacheKeyPrefix = "dns:"

type cachedResponse struct {
	Answer []string `json:"answer"`
	Rcode  int      `json:"rcode,omitempty"`
}

func cacheKey(name string, qtype uint16) string {
	return fmt.Sprintf("%s%s:%s", cacheKeyPrefix, strings.ToLower(dns.Fqdn(name)), dns.TypeToString[qtype])
}

func getCacheEntry(ctx context.Context, kdb *keydb.Client, name string, qtype uint16) (rrs []dns.RR, rcode int, hit bool, err error) {
	raw, err := kdb.Get(ctx, cacheKey(name, qtype))
	if err != nil || raw == nil {
		metrics.KeyDBCacheMisses.Inc()
		return nil, dns.RcodeSuccess, false, err
	}

	var cr cachedResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		metrics.KeyDBCacheMisses.Inc()
		return nil, dns.RcodeSuccess, false, nil
	}

	metrics.KeyDBCacheHits.Inc()

	for _, s := range cr.Answer {
		rr, err := dns.NewRR(s)
		if err == nil {
			rrs = append(rrs, rr)
		}
	}
	return rrs, cr.Rcode, true, nil
}

func setCacheEntry(ctx context.Context, kdb *keydb.Client, name string, qtype uint16, rrs []dns.RR, rcode int, negativeTTL time.Duration) error {
	texts := make([]string, 0, len(rrs))
	for _, rr := range rrs {
		texts = append(texts, rr.String())
	}

	raw, err := json.Marshal(cachedResponse{Answer: texts, Rcode: rcode})
	if err != nil {
		return err
	}

	var ttl time.Duration
	if rcode != dns.RcodeSuccess || len(rrs) == 0 {
		if negativeTTL == 0 {
			return nil
		}
		ttl = negativeTTL
	} else {
		minTTL := uint32(3600)
		for _, rr := range rrs {
			if t := rr.Header().Ttl; t < minTTL {
				minTTL = t
			}
		}
		if minTTL == 0 {
			return nil
		}
		ttl = time.Duration(minTTL) * time.Second
	}

	return kdb.Set(ctx, cacheKey(name, qtype), raw, ttl)
}

