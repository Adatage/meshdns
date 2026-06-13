package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	UDPEnabled bool
	TCPEnabled bool
	UDPPort    int
	TCPPort    int

	RecursiveEnabled bool

	GRPCAddr string

	CockroachDSN string

	KeyDBAddr     string
	KeyDBPassword string
	KeyDBDB       int

	MetricsAddr string
	NegativeTTL time.Duration
}

//	DNS_UDP_ENABLED       bool   (default: true)
//	DNS_TCP_ENABLED       bool   (default: true)
//	DNS_UDP_PORT          int    (default: 53)
//	DNS_TCP_PORT          int    (default: 53)
//	DNS_RECURSIVE_ENABLED bool   (default: false)
//	DNS_GRPC_ADDR         string (default: ":50051")
//	COCKROACH_DSN         string (optional)
//	KEYDB_ADDR            string (optional)
//	KEYDB_PASSWORD        string (optional)
//	KEYDB_DB              int    (default: 0)
func Load() (*Config, error) {
	udpPort, err := envInt("DNS_UDP_PORT", 53)
	if err != nil {
		return nil, fmt.Errorf("DNS_UDP_PORT: %w", err)
	}
	tcpPort, err := envInt("DNS_TCP_PORT", 53)
	if err != nil {
		return nil, fmt.Errorf("DNS_TCP_PORT: %w", err)
	}
	keydbDB, err := envInt("KEYDB_DB", 0)
	if err != nil {
		return nil, fmt.Errorf("KEYDB_DB: %w", err)
	}

	negTTL, err := envDuration("DNS_NEGATIVE_TTL", 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("DNS_NEGATIVE_TTL: %w", err)
	}

	c := &Config{
		UDPEnabled:       envBool("DNS_UDP_ENABLED", true),
		TCPEnabled:       envBool("DNS_TCP_ENABLED", true),
		UDPPort:          udpPort,
		TCPPort:          tcpPort,
		RecursiveEnabled: envBool("DNS_RECURSIVE_ENABLED", false),
		GRPCAddr:         envStr("DNS_GRPC_ADDR", ":50051"),
		CockroachDSN:     envStr("COCKROACH_DSN", ""),
		KeyDBAddr:        envStr("KEYDB_ADDR", ""),
		KeyDBPassword:    envStr("KEYDB_PASSWORD", ""),
		KeyDBDB:          keydbDB,
		MetricsAddr:      envStr("DNS_METRICS_ADDR", ""),
		NegativeTTL:      negTTL,
	}

	if !c.UDPEnabled && !c.TCPEnabled {
		return nil, fmt.Errorf("at least one of DNS_UDP_ENABLED or DNS_TCP_ENABLED must be true")
	}

	return c, nil
}

func (c *Config) AuthoritativeEnabled() bool {
	return c.CockroachDSN != ""
}

func (c *Config) CacheEnabled() bool {
	return c.KeyDBAddr != ""
}

func (c *Config) UDPAddr() string {
	return fmt.Sprintf(":%d", c.UDPPort)
}

func (c *Config) TCPAddr() string {
	return fmt.Sprintf(":%d", c.TCPPort)
}

func envStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q", v)
	}
	return n, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", v)
	}
	return d, nil
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return def
	}
	return v == "true" || v == "1" || v == "yes"
}
