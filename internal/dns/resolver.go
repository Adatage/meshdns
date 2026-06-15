package dnsserver

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const (
	resolverTimeout   = 3 * time.Second
	publicRaceTimeout = 2 * time.Second // per-public-resolver deadline
	resolveDeadline   = 5 * time.Second // overall Resolve() deadline
	maxResolveDepth   = 30
	rootHintsTTL      = 48 * time.Hour
	tldCacheTTL       = 24 * time.Hour // TLD delegations almost never change
	minDelegationTTL  = 60 * time.Second
)

// publicRecursiveResolvers are well-known fast public DNS services used to
// race against the iterative path. First valid answer across all wins.
var publicRecursiveResolvers = []string{
	"8.8.8.8:53",         // Google
	"8.8.4.4:53",         // Google
	"1.1.1.1:53",         // Cloudflare
	"1.0.0.1:53",         // Cloudflare
	"9.9.9.9:53",         // Quad9
	"149.112.112.112:53", // Quad9
	"208.67.222.222:53",  // OpenDNS
	"208.67.220.220:53",  // OpenDNS
	"64.6.64.6:53",       // Verisign
	"185.228.168.9:53",   // CleanBrowsing
}

// commonTLDs to pre-warm at startup so the first real query skips root servers.
var commonTLDs = []string{
	"com.", "net.", "org.", "io.", "dev.",
	"co.", "app.", "cloud.", "tech.", "ai.",
	"info.", "biz.", "me.", "us.", "uk.",
}

type nsCacheEntry struct {
	addrs     []string
	expiresAt time.Time
}

type Resolver struct {
	client    *dns.Client // used for iterative (non-recursive) queries
	pubClient *dns.Client // used for racing public resolvers (RD=true)
	nsCache   sync.Map
}

func NewResolver() *Resolver {
	r := &Resolver{
		client: &dns.Client{
			Net:     "udp",
			Timeout: resolverTimeout,
		},
		pubClient: &dns.Client{
			Net:     "udp",
			Timeout: publicRaceTimeout,
		},
	}
	r.nsCache.Store(".", &nsCacheEntry{
		addrs:     ipv4RootHints,
		expiresAt: time.Now().Add(rootHintsTTL),
	})
	// Pre-warm TLD NS cache in the background so the first real query
	// for common TLDs skips the root-server round-trip entirely.
	go r.prewarmTLDs()
	return r
}

// Resolve races iterative resolution against all public recursive resolvers
// and returns the first valid answer.
func (r *Resolver) Resolve(qname string, qtype uint16) (*dns.Msg, error) {
	qname = dns.Fqdn(strings.ToLower(qname))

	type result struct {
		msg *dns.Msg
		err error
	}

	total := 1 + len(publicRecursiveResolvers)
	ch := make(chan result, total)
	ctx, cancel := context.WithTimeout(context.Background(), resolveDeadline)
	defer cancel()

	send := func(res result) {
		select {
		case ch <- res:
		case <-ctx.Done():
		}
	}

	// Iterative resolver — authoritative walk starting from best cached point.
	go func() {
		msg, err := r.resolveIterative(qname, qtype)
		send(result{msg, err})
	}()

	// Race each public resolver in parallel with RD=1.
	for _, ns := range publicRecursiveResolvers {
		ns := ns
		go func() {
			m := new(dns.Msg)
			m.SetQuestion(qname, qtype)
			m.RecursionDesired = true
			resp, _, err := r.pubClient.Exchange(m, ns)
			send(result{resp, err})
		}()
	}

	received := 0
	var lastErr error
	for received < total {
		select {
		case res := <-ch:
			received++
			if res.err == nil && res.msg != nil && res.msg.Rcode != dns.RcodeServerFailure {
				return res.msg, nil
			}
			if res.err != nil {
				lastErr = res.err
			}
		case <-ctx.Done():
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, fmt.Errorf("resolution timed out for %s", qname)
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("all resolvers failed for %s", qname)
}

// resolveIterative performs a full iterative walk from the best cached
// delegation point down to the authoritative answer.
func (r *Resolver) resolveIterative(qname string, qtype uint16) (*dns.Msg, error) {
	nameservers := r.bestStart(qname)

	for depth := 0; depth < maxResolveDepth; depth++ {
		if len(nameservers) == 0 {
			return nil, fmt.Errorf("no nameservers left during resolution of %s", qname)
		}
		resp, err := r.queryAny(nameservers, qname, qtype)
		if err != nil {
			return nil, err
		}
		if resp.Rcode == dns.RcodeSuccess && len(resp.Answer) > 0 {
			return resp, nil
		}
		if resp.Rcode == dns.RcodeNameError {
			return resp, nil
		}
		if len(resp.Ns) > 0 {
			next, err := r.extractNS(resp)
			if err != nil || len(next) == 0 {
				return nil, fmt.Errorf("could not extract NS from referral for %s", qname)
			}
			r.storeDelegation(resp, next)
			nameservers = next
			continue
		}

		return nil, fmt.Errorf("resolution stalled for %s at depth %d", qname, depth)
	}

	return nil, fmt.Errorf("max resolution depth reached for %s", qname)
}

// prewarmTLDs queries root servers for common TLDs and seeds the NS cache,
// eliminating root-server round-trips for all subsequent queries to those TLDs.
func (r *Resolver) prewarmTLDs() {
	for _, tld := range commonTLDs {
		tld := tld
		go func() {
			m := new(dns.Msg)
			m.SetQuestion(tld, dns.TypeNS)
			m.RecursionDesired = false
			root := ipv4RootHints[rand.Intn(len(ipv4RootHints))]
			resp, _, err := r.client.Exchange(m, root)
			if err != nil || resp == nil || len(resp.Ns) == 0 {
				return
			}
			addrs, _ := r.extractNS(resp)
			if len(addrs) > 0 {
				r.storeDelegation(resp, addrs)
			}
		}()
	}
}

func (r *Resolver) bestStart(qname string) []string {
	labels := dns.SplitDomainName(qname)
	for i := 0; i <= len(labels); i++ {
		var zone string
		if i == len(labels) {
			zone = "."
		} else {
			zone = dns.Fqdn(strings.Join(labels[i:], "."))
		}
		if v, ok := r.nsCache.Load(zone); ok {
			entry := v.(*nsCacheEntry)
			if time.Now().Before(entry.expiresAt) {
				return shuffle(entry.addrs)
			}
			r.nsCache.Delete(zone)
		}
	}
	return shuffle(ipv4RootHints)
}

func (r *Resolver) storeDelegation(resp *dns.Msg, addrs []string) {
	if len(addrs) == 0 {
		return
	}
	for _, rr := range resp.Ns {
		ns, ok := rr.(*dns.NS)
		if !ok {
			continue
		}
		zone := strings.ToLower(ns.Hdr.Name)
		ttl := time.Duration(ns.Hdr.Ttl) * time.Second
		if ttl < minDelegationTTL {
			ttl = minDelegationTTL
		}
		// TLD delegations (single-label: com., net., org. …) almost never
		// change — cache them for a full day to skip root-server lookups.
		if len(dns.SplitDomainName(zone)) == 1 {
			ttl = tldCacheTTL
		}
		r.nsCache.Store(zone, &nsCacheEntry{
			addrs:     addrs,
			expiresAt: time.Now().Add(ttl),
		})
		return
	}
}

func (r *Resolver) queryAny(nameservers []string, qname string, qtype uint16) (*dns.Msg, error) {
	msg := new(dns.Msg)
	msg.SetQuestion(qname, qtype)
	msg.RecursionDesired = false

	var lastErr error
	for _, ns := range nameservers {
		resp, _, err := r.client.Exchange(msg, ns)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.Truncated {
			resp, err = r.retryTCP(msg, ns)
			if err != nil {
				lastErr = err
				continue
			}
		}
		return resp, nil
	}
	return nil, fmt.Errorf("all nameservers failed: %w", lastErr)
}

func (r *Resolver) retryTCP(msg *dns.Msg, ns string) (*dns.Msg, error) {
	tc := &dns.Client{Net: "tcp", Timeout: resolverTimeout}
	resp, _, err := tc.Exchange(msg, ns)
	return resp, err
}

func (r *Resolver) extractNS(resp *dns.Msg) ([]string, error) {
	glue := make(map[string][]string)
	for _, rr := range resp.Extra {
		switch v := rr.(type) {
		case *dns.A:
			host := strings.ToLower(v.Hdr.Name)
			glue[host] = append(glue[host], net.JoinHostPort(v.A.String(), "53"))
		case *dns.AAAA:
			host := strings.ToLower(v.Hdr.Name)
			glue[host] = append(glue[host], net.JoinHostPort(v.AAAA.String(), "53"))
		}
	}

	// Detect delegation zone for in-bailiwick check below.
	var delegationZone string
	for _, rr := range resp.Ns {
		if ns, ok := rr.(*dns.NS); ok {
			delegationZone = strings.ToLower(ns.Hdr.Name)
			break
		}
	}

	var addrs []string
	for _, rr := range resp.Ns {
		ns, ok := rr.(*dns.NS)
		if !ok {
			continue
		}
		host := strings.ToLower(ns.Ns)
		if ips, found := glue[host]; found {
			addrs = append(addrs, ips...)
			continue
		}
		// Skip in-bailiwick NS records with no glue — resolving them would
		// recurse into the same unresolvable zone.
		if delegationZone != "" && dns.IsSubDomain(delegationZone, host) {
			continue
		}
		// Use iterative (not the racing Resolve) to avoid nested races.
		resolved, err := r.resolveIterative(host, dns.TypeA)
		if err != nil || resolved == nil {
			continue
		}
		for _, rr2 := range resolved.Answer {
			if a, ok := rr2.(*dns.A); ok {
				addrs = append(addrs, net.JoinHostPort(a.A.String(), "53"))
			}
		}
	}

	return shuffle(addrs), nil
}

func shuffle(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}
