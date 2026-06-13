package dnsserver

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/Adatage/meshdns/internal/cockroach"
	"github.com/Adatage/meshdns/internal/metrics"
	"github.com/Adatage/meshdns/internal/zonecache"
)

type Authoritative struct {
	db          *cockroach.DB
	cache       *zonecache.Cache
	negativeTTL time.Duration
}

func NewAuthoritative(db *cockroach.DB, zc *zonecache.Cache, negativeTTL time.Duration) *Authoritative {
	return &Authoritative{db: db, cache: zc, negativeTTL: negativeTTL}
}

func (a *Authoritative) findZone(ctx context.Context, qname string) (string, error) {
	qname = strings.ToLower(dns.Fqdn(qname))

	zones, ok := a.cache.GetZones()
	if !ok {
		var err error
		zones, err = a.db.ZoneNames(ctx)
		if err != nil {
			return "", err
		}
		a.cache.SetZones(zones, 30*time.Second)
	}

	best := ""
	for _, z := range zones {
		zfqdn := dns.Fqdn(z)
		if strings.HasSuffix(qname, zfqdn) && len(zfqdn) > len(dns.Fqdn(best)) {
			best = z
		}
	}
	return best, nil
}

func (a *Authoritative) Answer(ctx context.Context, m *dns.Msg, q dns.Question, clientIP net.IP) (bool, error) {
	qname := strings.ToLower(dns.Fqdn(q.Name))
	qtype := dns.TypeToString[q.Qtype]

	zoneName, err := a.findZone(ctx, qname)
	if err != nil {
		return false, err
	}
	if zoneName == "" {
		return false, nil
	}

	if e, ok := a.cache.GetRecord(qname, q.Qtype); ok {
		metrics.ZoneCacheHits.Inc()
		m.Authoritative = true
		a.applyEntry(m, e, clientIP)
		return true, nil
	}
	metrics.ZoneCacheMisses.Inc()

	recs, err := a.db.LookupRecords(ctx, qname, qtype)
	if err != nil {
		return true, err
	}

	m.Authoritative = true

	if len(recs) > 0 {
		entry := buildEntry(recs)
		rrs := entry.RRsForClient(clientIP)
		m.Answer = append(m.Answer, rrs...)
		ttl := minTTLFromEntry(entry)
		a.cache.SetRecord(qname, q.Qtype, entry, ttl)
		return true, nil
	}

	soa, _ := a.getSOA(ctx, zoneName)
	if soa == nil {
		soa = a.synthesizeSOA(zoneName)
	}

	exists, err := a.db.NameExistsInZone(ctx, zoneName, qname)
	if err != nil {
		return true, err
	}

	var entry *zonecache.Entry
	if exists {
		m.SetRcode(m, dns.RcodeSuccess)
		if soa != nil {
			m.Ns = append(m.Ns, soa)
		}
		entry = &zonecache.Entry{NoData: true, SOA: soa}
	} else {
		m.SetRcode(m, dns.RcodeNameError)
		if soa != nil {
			m.Ns = append(m.Ns, soa)
		}
		entry = &zonecache.Entry{NXDomain: true, SOA: soa}
	}

	a.cache.SetRecord(qname, q.Qtype, entry, a.negativeTTL)
	return true, nil
}

func (a *Authoritative) applyEntry(m *dns.Msg, e *zonecache.Entry, clientIP net.IP) {
	if e.NXDomain {
		m.SetRcode(m, dns.RcodeNameError)
		if e.SOA != nil {
			m.Ns = append(m.Ns, e.SOA)
		}
		return
	}
	if e.NoData {
		m.SetRcode(m, dns.RcodeSuccess)
		if e.SOA != nil {
			m.Ns = append(m.Ns, e.SOA)
		}
		return
	}
	m.Answer = append(m.Answer, e.RRsForClient(clientIP)...)
}

func buildEntry(recs []cockroach.Record) *zonecache.Entry {
	type subnetGroup struct {
		net *net.IPNet
		rrs []dns.RR
	}
	subnetMap := map[string]*subnetGroup{}
	var globalRRs []dns.RR

	for _, rec := range recs {
		rr, err := parseRecord(rec)
		if err != nil {
			continue
		}
		if rec.Subnet == nil || *rec.Subnet == "" {
			globalRRs = append(globalRRs, rr)
		} else {
			_, ipnet, err := net.ParseCIDR(*rec.Subnet)
			if err != nil {
				continue
			}
			cidr := ipnet.String()
			if subnetMap[cidr] == nil {
				subnetMap[cidr] = &subnetGroup{net: ipnet}
			}
			subnetMap[cidr].rrs = append(subnetMap[cidr].rrs, rr)
		}
	}

	subnetRRs := make([]zonecache.SubnetRR, 0, len(subnetMap))
	for _, sg := range subnetMap {
		subnetRRs = append(subnetRRs, zonecache.SubnetRR{Net: sg.net, RRs: sg.rrs})
	}
	sort.Slice(subnetRRs, func(i, j int) bool {
		oi, _ := subnetRRs[i].Net.Mask.Size()
		oj, _ := subnetRRs[j].Net.Mask.Size()
		return oi > oj
	})

	return &zonecache.Entry{
		SubnetRRs: subnetRRs,
		GlobalRRs: globalRRs,
	}
}

func minTTLFromEntry(e *zonecache.Entry) time.Duration {
	min := uint32(3600)
	check := func(rrs []dns.RR) {
		for _, rr := range rrs {
			if t := rr.Header().Ttl; t > 0 && t < min {
				min = t
			}
		}
	}
	for _, s := range e.SubnetRRs {
		check(s.RRs)
	}
	check(e.GlobalRRs)
	if min == 0 {
		return 60 * time.Second
	}
	return time.Duration(min) * time.Second
}

func (a *Authoritative) getSOA(ctx context.Context, zoneName string) (dns.RR, error) {
	rec, err := a.db.GetSOA(ctx, zoneName)
	if err != nil || rec == nil {
		return nil, err
	}
	return parseRecord(*rec)
}

func (a *Authoritative) synthesizeSOA(zoneName string) dns.RR {
	zfqdn := dns.Fqdn(zoneName)
	line := fmt.Sprintf("%s 300 IN SOA ns1.%s hostmaster.%s 1 3600 900 604800 300",
		zfqdn, zfqdn, zfqdn)
	rr, err := dns.NewRR(line)
	if err != nil {
		return nil
	}
	return rr
}

func parseRecord(rec cockroach.Record) (dns.RR, error) {
	name := dns.Fqdn(strings.ToLower(rec.Name))
	line := fmt.Sprintf("%s %d IN %s %s", name, rec.TTL, rec.Type, rec.Data)
	rr, err := dns.NewRR(line)
	if err != nil {
		return nil, fmt.Errorf("parse record %q: %w", line, err)
	}
	return rr, nil
}
