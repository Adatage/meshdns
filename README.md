# MeshDNS

A production-grade DNS server written in Go, built for modern infrastructure.  
MeshDNS can act as a **pure recursor**, an **authoritative server**, or both simultaneously — all controlled through environment variables with zero config files required.

It supports **subnet-based split-horizon DNS** (different answers per client CIDR), in-memory zone caching, negative caching, optional Prometheus metrics, and a full gRPC control plane with a companion CLI (`dnsctl`).

## Features

| Feature | Description |
|---|---|
| UDP & TCP | Dual-stack DNS listeners, independently configurable |
| Recursive resolver | Iterative resolution from IANA root hints with NS delegation caching |
| Authoritative zones | Serve zones and records stored in CockroachDB |
| Split-horizon DNS | Per-record subnet CIDR — different answers for different client networks |
| Zone cache | In-memory zone and record cache with smart invalidation on every gRPC mutation |
| Negative caching | NXDOMAIN and NODATA responses cached with a configurable short TTL |
| KeyDB cache | Redis-compatible caching of recursive responses |
| SOA support | Store SOA records or have them synthesised automatically |
| Prometheus metrics | Optional `/metrics` endpoint — queries, latency, cache hit/miss rates |
| gRPC control plane | Full zone/record CRUD + status endpoint with server reflection |
| `dnsctl` CLI | Human-friendly CLI backed by gRPC |
| Docker Compose | One-command full-stack deployment (CockroachDB + KeyDB + Prometheus) |

---

## Quick Start

### Docker Compose (recommended)

```bash
git clone https://github.com/Adatage/meshdns.git
cd meshdns

sudo docker compose up --build -d
```

This starts:
- **CockroachDB** at `172.30.0.10:26257` (HTTP admin UI on `localhost:8080`)
- **KeyDB** at `172.30.0.11:6379`
- **MeshDNS** at `172.30.0.20` — UDP/TCP port 53, gRPC port 50051, metrics port 9153
- **Prometheus** at `localhost:9090` (scrapes MeshDNS metrics every 15 s)

The schema is applied automatically on first start via `cockroach-init`.

```bash
# Verify everything is up
sudo docker compose ps

# Try a query
dig @127.0.0.1 google.com A
```

### Local binary

```bash
make deps
make build          # produces bin/dns-server and bin/dnsctl

# Needs root or CAP_NET_BIND_SERVICE for port 53
sudo setcap cap_net_bind_service=+ep bin/dns-server

COCKROACH_DSN="postgres://root@localhost:26257/dns?sslmode=disable" \
KEYDB_ADDR="localhost:6379" \
DNS_RECURSIVE_ENABLED=true \
./bin/dns-server
```

---

## Configuration

All settings are read from **environment variables**.

### DNS transport

| Variable | Default | Description |
|---|---|---|
| `DNS_UDP_ENABLED` | `true` | Enable UDP listener |
| `DNS_TCP_ENABLED` | `true` | Enable TCP listener |
| `DNS_UDP_PORT` | `53` | UDP bind port |
| `DNS_TCP_PORT` | `53` | TCP bind port |

### Resolver

| Variable | Default | Description |
|---|---|---|
| `DNS_RECURSIVE_ENABLED` | `false` | Enable iterative recursive resolution from root hints |

### gRPC control plane

| Variable | Default | Description |
|---|---|---|
| `DNS_GRPC_ADDR` | `:50051` | gRPC listen address |

### CockroachDB — authoritative mode (optional)

Authoritative mode is **automatically enabled** when `COCKROACH_DSN` is set.

| Variable | Default | Description |
|---|---|---|
| `COCKROACH_DSN` | _(unset)_ | e.g. `postgres://root@localhost:26257/dns?sslmode=disable` |

Apply the schema before the first run (not needed with Docker Compose):

```bash
cockroach sql --url "$COCKROACH_DSN" -f sql/create.sql
```

To migrate an existing database (adds the `subnet` column):

```bash
cockroach sql --url "$COCKROACH_DSN" -f sql/migrate_001_subnet.sql
```

### KeyDB / Redis — recursive cache (optional)

| Variable | Default | Description |
|---|---|---|
| `KEYDB_ADDR` | _(unset)_ | `host:port`, e.g. `localhost:6379` |
| `KEYDB_PASSWORD` | _(unset)_ | Password (leave empty if none) |
| `KEYDB_DB` | `0` | Database index |

### Negative caching

| Variable | Default | Description |
|---|---|---|
| `DNS_NEGATIVE_TTL` | `30s` | TTL for NXDOMAIN and NODATA responses in both the zone cache and KeyDB |

### Prometheus metrics (optional)

| Variable | Default | Description |
|---|---|---|
| `DNS_METRICS_ADDR` | _(unset)_ | HTTP address to expose `/metrics`, e.g. `:9153` |

---

## Query Pipeline

Every incoming DNS query is processed in this order:

```
Query received
  │
  ├─ 1. Authoritative lookup                 (only if COCKROACH_DSN is set)
  │       zone cache hit  → subnet-select RRs → answer (AA)
  │       zone cache miss → CockroachDB lookup → populate cache → subnet-select RRs → answer (AA)
  │       NXDOMAIN / NODATA → SOA in Authority section → negative cache entry
  │       no zone match   → continue
  │
  ├─ 2. KeyDB cache lookup                   (only if KEYDB_ADDR is set)
  │       hit  → return cached answer
  │       miss → continue
  │
  ├─ 3. Recursive resolution                 (only if DNS_RECURSIVE_ENABLED=true)
  │       iterative walk: root → TLD → authoritative NS
  │       NS delegation results cached in memory for subsequent queries
  │       response stored in KeyDB (positive and NXDOMAIN)
  │
  └─ 4. REFUSED  (nothing matched)
```

---

## Split-horizon DNS

Records with a `subnet` field are returned **only to clients whose IP falls inside that CIDR**.  
A `NULL` subnet record acts as the global fallback.

**Priority:** most-specific subnet match → global fallback.

Example:
```bash
# Global record — returned to everyone not matched by a subnet rule
dnsctl record add --zone example.com --name api.example.com --type A --data 203.0.113.1 --ttl 300

# Subnet-specific record — returned only to 10.0.0.0/8 clients
dnsctl record add --zone example.com --name api.example.com --type A --data 10.10.0.1  --ttl 300 --subnet 10.0.0.0/8

# A client from 10.5.5.5  → receives 10.10.0.1
# A client from 8.8.8.8   → receives 203.0.113.1
```

Multiple subnets can exist for the same record name — the most-specific prefix (longest mask) is matched first.

---

## gRPC API

The proto definition lives in [api/proto/dns.proto](api/proto/dns.proto).  
Generated Go code is in [pkg/proto/](pkg/proto/).

### Service methods

| RPC | Description |
|---|---|
| `CreateZone` | Create an authoritative zone |
| `DeleteZone` | Delete a zone and all its records |
| `ListZones` | List all zones |
| `AddRecord` | Add a DNS record (optionally with a subnet CIDR) |
| `DeleteRecord` | Delete a record by UUID |
| `ListRecords` | List all records in a zone |
| `GetStatus` | Server status, feature flags, uptime |

The server registers gRPC reflection, so tools like **grpcurl**, **Evans**, or **Postman** work without the proto file:

```bash
grpcurl -plaintext localhost:50051 list
grpcurl -plaintext localhost:50051 dnscontrol.v1.DNSControl/GetStatus
grpcurl -plaintext -d '{"name":"example.com"}' localhost:50051 dnscontrol.v1.DNSControl/CreateZone
```

### Regenerate proto files

```bash
make proto-tools   # install protoc-gen-go and protoc-gen-go-grpc (once)
make proto         # regenerate pkg/proto/
```

---

## `dnsctl` CLI

```
dnsctl [--addr host:port] <command>

Commands:
  status                    Show server status and feature flags
  zone list                 List all authoritative zones
  zone create <name>        Create a new zone
  zone delete <name>        Delete a zone and all its records
  record list <zone>        List all records in a zone
  record add                Add a record to a zone
  record delete <id>        Delete a record by UUID

Flags:
  --addr   gRPC server address (default: localhost:50051, env: DNS_GRPC_ADDR)
```

### `record add` flags

| Flag | Required | Description |
|---|---|---|
| `--zone` | yes | Zone name (e.g. `example.com`) |
| `--name` | yes | Record name FQDN (e.g. `www.example.com`) |
| `--type` | yes | Record type: `A`, `AAAA`, `MX`, `CNAME`, `TXT`, `NS`, `SRV`, `SOA`, … |
| `--data` | yes | Rdata string (e.g. `1.2.3.4`, `"10 mail.example.com."`) |
| `--ttl`  | no  | TTL in seconds (default `300`) |
| `--subnet` | no | CIDR for split-horizon (e.g. `10.0.0.0/8`) |

### Examples

```bash
# Status
dnsctl status

# Create a zone
dnsctl zone create example.com

# Add records
dnsctl record add --zone example.com --name example.com   --type A   --ttl 300 --data 203.0.113.1
dnsctl record add --zone example.com --name www.example.com --type A --ttl 300 --data 203.0.113.2
dnsctl record add --zone example.com --name example.com   --type MX  --ttl 300 --data "10 mail.example.com."
dnsctl record add --zone example.com --name example.com   --type SOA --ttl 300 --data "ns1.example.com. hostmaster.example.com. 2024010101 3600 900 604800 300"

# Split-horizon A record (internal network gets a different IP)
dnsctl record add --zone example.com --name api.example.com --type A --data 10.10.0.5 --subnet 10.0.0.0/8

# List records (SUBNET column shows split-horizon entries)
dnsctl record list example.com

# Delete a record
dnsctl record delete <uuid>
```

---

## Prometheus Metrics

When `DNS_METRICS_ADDR` is set MeshDNS exposes a `/metrics` endpoint:

| Metric | Type | Description |
|---|---|---|
| `dns_queries_total{qtype,rcode}` | Counter | Total queries by type and response code |
| `dns_query_duration_seconds{qtype}` | Histogram | Query latency by type |
| `dns_zone_cache_hits_total` | Counter | In-memory zone cache hits |
| `dns_zone_cache_misses_total` | Counter | In-memory zone cache misses |
| `dns_keydb_cache_hits_total` | Counter | KeyDB cache hits |
| `dns_keydb_cache_misses_total` | Counter | KeyDB cache misses |

Check raw metrics:
```bash
curl -s http://localhost:9153/metrics | grep ^dns_
```

With Docker Compose, the Prometheus UI is at **http://localhost:9090**. Useful queries:

```promql
rate(dns_queries_total[1m])
histogram_quantile(0.99, rate(dns_query_duration_seconds_bucket[5m]))
rate(dns_zone_cache_hits_total[1m]) / (rate(dns_zone_cache_hits_total[1m]) + rate(dns_zone_cache_misses_total[1m]))
```

---

## Project Structure

```
.
├── api/
│   └── proto/
│       └── dns.proto              # gRPC service + message definitions
├── cmd/
│   ├── cli/
│   │   └── main.go                # dnsctl CLI entry point
│   └── server/
│       └── main.go                # DNS + gRPC server entry point
├── internal/
│   ├── config/
│   │   └── config.go              # Environment variable configuration
│   ├── cockroach/
│   │   └── cockroach.go           # CockroachDB zone/record storage
│   ├── dns/
│   │   ├── server.go              # DNS listener management
│   │   ├── handler.go             # Query pipeline (auth → cache → recurse)
│   │   ├── resolver.go            # Iterative recursive resolver with NS cache
│   │   ├── authoritative.go       # Authoritative zone answers + split-horizon
│   │   └── cache.go               # KeyDB cache helpers
│   ├── grpc/
│   │   └── server.go              # gRPC DNSControl implementation
│   ├── keydb/
│   │   └── keydb.go               # KeyDB client wrapper
│   ├── metrics/
│   │   └── metrics.go             # Prometheus metrics
│   └── zonecache/
│       └── zonecache.go           # In-memory zone + record cache
├── pkg/
│   └── proto/
│       ├── dns.pb.go              # Generated protobuf messages
│       └── dns_grpc.pb.go         # Generated gRPC stubs
├── prometheus/
│   └── prometheus.yml             # Prometheus scrape config
├── sql/
│   ├── create.sql                 # CockroachDB schema
│   └── migrate_001_subnet.sql     # Migration: add subnet column
├── Dockerfile
├── docker-compose.yml
├── go.mod
└── Makefile
```

---

## Development

```bash
make deps        # tidy Go dependencies
make build       # build binaries → bin/dns-server and bin/dnsctl
make proto       # regenerate gRPC code from api/proto/dns.proto
make clean       # remove build artefacts
make help        # show all targets
```

### Third-party gRPC integration

```go
import (
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
    pb "github.com/Adatage/meshdns/pkg/proto"
)

conn, _ := grpc.NewClient("dns-server:50051",
    grpc.WithTransportCredentials(insecure.NewCredentials()))
client := pb.NewDNSControlClient(conn)

resp, _ := client.AddRecord(ctx, &pb.AddRecordRequest{
    ZoneName: "example.com",
    Name:     "api.example.com",
    Type:     "A",
    Ttl:      300,
    Data:     "203.0.113.1",
    Subnet:   "10.0.0.0/8", // optional
})
```

---

## License

Apache-2.0
