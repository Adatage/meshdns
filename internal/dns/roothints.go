package dnsserver

// rootHints is a static list of the 13 DNS root name servers (IPv4 + IPv6).
// These are used as the starting point for iterative recursive resolution.
// Source: https://www.iana.org/domains/root/servers
var rootHints = []string{
	// a.root-servers.net – Verisign
	"198.41.0.4:53",
	"2001:503:ba3e::2:30:53",
	// b.root-servers.net – USC-ISI
	"170.247.170.2:53",
	"2801:1b8:10::b:53",
	// c.root-servers.net – Cogent
	"192.33.4.12:53",
	"2001:500:2::c:53",
	// d.root-servers.net – UMD
	"199.7.91.13:53",
	"2001:500:2d::d:53",
	// e.root-servers.net – NASA
	"192.203.230.10:53",
	"2001:500:a8::e:53",
	// f.root-servers.net – ISC
	"192.5.5.241:53",
	"2001:500:2f::f:53",
	// g.root-servers.net – DISA
	"192.112.36.4:53",
	"2001:500:12::d0d:53",
	// h.root-servers.net – ARL
	"198.97.190.53:53",
	"2001:500:1::53:53",
	// i.root-servers.net – Netnod
	"192.36.148.17:53",
	"2001:7fe::53:53",
	// j.root-servers.net – Verisign
	"192.58.128.30:53",
	"2001:503:c27::2:30:53",
	// k.root-servers.net – RIPE NCC
	"193.0.14.129:53",
	"2001:7fd::1:53",
	// l.root-servers.net – ICANN
	"199.7.83.42:53",
	"2001:500:9f::42:53",
	// m.root-servers.net – WIDE
	"202.12.27.33:53",
	"2001:dc3::35:53",
}

// ipv4RootHints returns only IPv4 root server addresses (safe fallback).
var ipv4RootHints = []string{
	"198.41.0.4:53",
	"170.247.170.2:53",
	"192.33.4.12:53",
	"199.7.91.13:53",
	"192.203.230.10:53",
	"192.5.5.241:53",
	"192.112.36.4:53",
	"198.97.190.53:53",
	"192.36.148.17:53",
	"192.58.128.30:53",
	"193.0.14.129:53",
	"199.7.83.42:53",
	"202.12.27.33:53",
}
