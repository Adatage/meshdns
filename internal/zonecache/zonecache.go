package zonecache

import (
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type SubnetRR struct {
	Net *net.IPNet
	RRs []dns.RR
}

type Entry struct {
	SubnetRRs []SubnetRR
	GlobalRRs []dns.RR
	NXDomain  bool
	NoData    bool
	SOA       dns.RR
	exp       time.Time
}

func (e *Entry) Live() bool {
	return time.Now().Before(e.exp)
}

func (e *Entry) RRsForClient(ip net.IP) []dns.RR {
	if ip != nil {
		for _, s := range e.SubnetRRs {
			if s.Net != nil && s.Net.Contains(ip) {
				return s.RRs
			}
		}
	}
	return e.GlobalRRs
}

type Cache struct {
	zonesMu  sync.RWMutex
	zones    []string
	zonesExp time.Time

	recMu sync.RWMutex
	recs  map[string]*Entry
}

func New() *Cache {
	return &Cache{recs: make(map[string]*Entry)}
}

func recKey(name string, qtype uint16) string {
	return strings.ToLower(dns.Fqdn(name)) + ":" + dns.TypeToString[qtype]
}

func (c *Cache) GetZones() ([]string, bool) {
	c.zonesMu.RLock()
	defer c.zonesMu.RUnlock()
	if c.zones == nil || time.Now().After(c.zonesExp) {
		return nil, false
	}
	out := make([]string, len(c.zones))
	copy(out, c.zones)
	return out, true
}

func (c *Cache) SetZones(names []string, ttl time.Duration) {
	c.zonesMu.Lock()
	defer c.zonesMu.Unlock()
	c.zones = make([]string, len(names))
	copy(c.zones, names)
	c.zonesExp = time.Now().Add(ttl)
}

func (c *Cache) InvalidateZoneList() {
	c.zonesMu.Lock()
	defer c.zonesMu.Unlock()
	c.zones = nil
	c.zonesExp = time.Time{}
}

func (c *Cache) GetRecord(name string, qtype uint16) (*Entry, bool) {
	c.recMu.RLock()
	defer c.recMu.RUnlock()
	e, ok := c.recs[recKey(name, qtype)]
	if !ok || !e.Live() {
		return nil, false
	}
	return e, true
}

func (c *Cache) SetRecord(name string, qtype uint16, e *Entry, ttl time.Duration) {
	e.exp = time.Now().Add(ttl)
	c.recMu.Lock()
	defer c.recMu.Unlock()
	c.recs[recKey(name, qtype)] = e
}

func (c *Cache) InvalidateName(name string, qtype uint16) {
	c.recMu.Lock()
	defer c.recMu.Unlock()
	delete(c.recs, recKey(name, qtype))
}

func (c *Cache) InvalidateZone(zoneName string) {
	zoneFQDN := strings.ToLower(dns.Fqdn(zoneName))
	suffix := "." + zoneFQDN

	c.recMu.Lock()
	defer c.recMu.Unlock()
	for k := range c.recs {
		namePart := strings.SplitN(k, ":", 2)[0]
		if namePart == zoneFQDN || strings.HasSuffix(namePart, suffix) {
			delete(c.recs, k)
		}
	}
}
