package cockroach

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const connectTimeout = 10 * time.Second

type DB struct {
	pool *pgxpool.Pool
}

type Zone struct {
	ID        string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Record struct {
	ID        string
	ZoneID    string
	ZoneName  string
	Name      string
	Type      string
	TTL       uint32
	Data      string
	Subnet    *string
	CreatedAt time.Time
}

func New(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("cockroach parse dsn: %w", err)
	}
	cfg.ConnConfig.ConnectTimeout = connectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("cockroach connect: %w", err)
	}

	ctxPing, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := pool.Ping(ctxPing); err != nil {
		pool.Close()
		return nil, fmt.Errorf("cockroach ping: %w", err)
	}

	return &DB{pool: pool}, nil
}

func (db *DB) Close() {
	db.pool.Close()
}

func (db *DB) CreateZone(ctx context.Context, name string) (*Zone, error) {
	z := &Zone{}
	err := db.pool.QueryRow(ctx,
		`INSERT INTO zones (name) VALUES ($1)
		 RETURNING id, name, created_at, updated_at`,
		name,
	).Scan(&z.ID, &z.Name, &z.CreatedAt, &z.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create zone %q: %w", name, err)
	}
	return z, nil
}

func (db *DB) DeleteZone(ctx context.Context, name string) error {
	tag, err := db.pool.Exec(ctx, `DELETE FROM zones WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("delete zone %q: %w", name, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("zone %q not found", name)
	}
	return nil
}

func (db *DB) ListZones(ctx context.Context) ([]Zone, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, name, created_at, updated_at FROM zones ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list zones: %w", err)
	}
	defer rows.Close()

	var zones []Zone
	for rows.Next() {
		var z Zone
		if err := rows.Scan(&z.ID, &z.Name, &z.CreatedAt, &z.UpdatedAt); err != nil {
			return nil, err
		}
		zones = append(zones, z)
	}
	return zones, rows.Err()
}

func (db *DB) GetZoneByName(ctx context.Context, name string) (*Zone, error) {
	z := &Zone{}
	err := db.pool.QueryRow(ctx,
		`SELECT id, name, created_at, updated_at FROM zones WHERE rtrim(name, '.') = rtrim($1, '.')`, name,
	).Scan(&z.ID, &z.Name, &z.CreatedAt, &z.UpdatedAt)
	if err != nil {
		return nil, nil
	}
	return z, nil
}

func fqdn(name string) string {
	if !strings.HasSuffix(name, ".") {
		return name + "."
	}
	return name
}

func (db *DB) AddRecord(ctx context.Context, zoneName, name, rtype string, ttl uint32, data string, subnet *string) (*Record, error) {
	zoneName = strings.TrimSuffix(fqdn(zoneName), ".")
	name = fqdn(name)
	if subnet != nil && *subnet == "" {
		subnet = nil
	}
	r := &Record{}
	err := db.pool.QueryRow(ctx,
		`INSERT INTO records (zone_id, name, type, ttl, data, subnet)
		 SELECT z.id, $2, $3, $4, $5, $6 FROM zones z WHERE z.name = $1
		 RETURNING id, zone_id, $1, name, type, ttl, data, subnet, created_at`,
		zoneName, name, rtype, ttl, data, subnet,
	).Scan(&r.ID, &r.ZoneID, &r.ZoneName, &r.Name, &r.Type, &r.TTL, &r.Data, &r.Subnet, &r.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("add record to zone %q: %w", zoneName, err)
	}
	return r, nil
}

func (db *DB) DeleteRecord(ctx context.Context, id string) error {
	tag, err := db.pool.Exec(ctx, `DELETE FROM records WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete record %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("record %q not found", id)
	}
	return nil
}

func (db *DB) ListRecords(ctx context.Context, zoneName string) ([]Record, error) {
	zoneName = strings.TrimSuffix(zoneName, ".")
	rows, err := db.pool.Query(ctx,
		`SELECT r.id, r.zone_id, z.name, r.name, r.type, r.ttl, r.data, r.subnet, r.created_at
		 FROM records r JOIN zones z ON r.zone_id = z.id
		 WHERE rtrim(z.name, '.') = $1
		 ORDER BY r.name, r.type`,
		zoneName,
	)
	if err != nil {
		return nil, fmt.Errorf("list records for zone %q: %w", zoneName, err)
	}
	defer rows.Close()

	var records []Record
	for rows.Next() {
		var rec Record
		if err := rows.Scan(&rec.ID, &rec.ZoneID, &rec.ZoneName,
			&rec.Name, &rec.Type, &rec.TTL, &rec.Data, &rec.Subnet, &rec.CreatedAt); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

func (db *DB) LookupRecords(ctx context.Context, name, rtype string) ([]Record, error) {
	normName := strings.TrimSuffix(name, ".")
	rows, err := db.pool.Query(ctx,
		`SELECT r.id, r.zone_id, z.name, r.name, r.type, r.ttl, r.data, r.subnet, r.created_at
		 FROM records r JOIN zones z ON r.zone_id = z.id
		 WHERE rtrim(r.name, '.') = $1 AND r.type = $2
		 ORDER BY (r.subnet IS NOT NULL) DESC`,
		normName, rtype,
	)
	if err != nil {
		return nil, fmt.Errorf("lookup %s %s: %w", rtype, name, err)
	}
	defer rows.Close()

	var records []Record
	for rows.Next() {
		var rec Record
		if err := rows.Scan(&rec.ID, &rec.ZoneID, &rec.ZoneName,
			&rec.Name, &rec.Type, &rec.TTL, &rec.Data, &rec.Subnet, &rec.CreatedAt); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

func (db *DB) ZoneNames(ctx context.Context) ([]string, error) {
	rows, err := db.pool.Query(ctx, `SELECT name FROM zones ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

func (db *DB) GetRecordByID(ctx context.Context, id string) (*Record, error) {
	r := &Record{}
	err := db.pool.QueryRow(ctx,
		`SELECT r.id, r.zone_id, z.name, r.name, r.type, r.ttl, r.data, r.subnet, r.created_at
		 FROM records r JOIN zones z ON r.zone_id = z.id
		 WHERE r.id = $1`, id,
	).Scan(&r.ID, &r.ZoneID, &r.ZoneName, &r.Name, &r.Type, &r.TTL, &r.Data, &r.Subnet, &r.CreatedAt)
	if err != nil {
		return nil, nil
	}
	return r, nil
}

func (db *DB) GetSOA(ctx context.Context, zoneName string) (*Record, error) {
	zoneName = strings.TrimSuffix(zoneName, ".")
	r := &Record{}
	err := db.pool.QueryRow(ctx,
		`SELECT r.id, r.zone_id, z.name, r.name, r.type, r.ttl, r.data, r.subnet, r.created_at
		 FROM records r JOIN zones z ON r.zone_id = z.id
		 WHERE rtrim(z.name, '.') = $1 AND r.type = 'SOA'
		 LIMIT 1`, zoneName,
	).Scan(&r.ID, &r.ZoneID, &r.ZoneName, &r.Name, &r.Type, &r.TTL, &r.Data, &r.Subnet, &r.CreatedAt)
	if err != nil {
		return nil, nil
	}
	return r, nil
}

func (db *DB) NameExistsInZone(ctx context.Context, zoneName, name string) (bool, error) {
	zoneName = strings.TrimSuffix(zoneName, ".")
	name = strings.TrimSuffix(name, ".")
	var exists bool
	err := db.pool.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM records r JOIN zones z ON r.zone_id = z.id
			WHERE rtrim(z.name, '.') = $1 AND rtrim(r.name, '.') = $2
		)`, zoneName, name,
	).Scan(&exists)
	return exists, err
}

