package dnsserver

import (
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const (
	resolverTimeout = 3 * time.Second
	maxResolveDepth = 30
	rootHintsTTL = 48 * time.Hour
	minDelegationTTL = 60 * time.Second
)

type nsCacheEntry struct {
	addrs     []string
	expiresAt time.Time
}

type Resolver struct {
	client  *dns.Client
	nsCache sync.Map
}

func NewResolver() *Resolver {
	r := &Resolver{
		client: &dns.Client{
			Net:     "udp",
			Timeout: resolverTimeout,
		},
	}
	
	r.nsCache.Store(".", &nsCacheEntry{
		addrs:     ipv4RootHints,
		expiresAt: time.Now().Add(rootHintsTTL),
	})
	return r
}

func (r *Resolver) Resolve(qname string, qtype uint16) (*dns.Msg, error) {
	qname = dns.Fqdn(strings.ToLower(qname))
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

	// Determine the zone being delegated to avoid resolving in-bailiwick NS
	// records that would cause infinite recursion when glue is absent.
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
		resolved, err := r.Resolve(host, dns.TypeA)
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
