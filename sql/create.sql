CREATE DATABASE IF NOT EXISTS dns;

USE dns;

CREATE TABLE IF NOT EXISTS zones (
    id         UUID        NOT NULL DEFAULT gen_random_uuid(),
    name       STRING(255) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_zones PRIMARY KEY (id),
    CONSTRAINT uq_zones_name UNIQUE (name)
);

CREATE TABLE IF NOT EXISTS records (
    id         UUID        NOT NULL DEFAULT gen_random_uuid(),
    zone_id    UUID        NOT NULL,
    name       STRING(255) NOT NULL,
    type       STRING(10)  NOT NULL,
    ttl        INT4        NOT NULL DEFAULT 300,
    data       STRING      NOT NULL,
    subnet     STRING(50)  NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_records    PRIMARY KEY (id),
    CONSTRAINT fk_records_zone FOREIGN KEY (zone_id) REFERENCES zones (id) ON DELETE CASCADE,
    INDEX idx_records_lookup (zone_id, name, type, subnet)
);
