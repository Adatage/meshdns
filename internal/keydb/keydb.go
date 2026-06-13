package keydb

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const pingTimeout = 5 * time.Second

type Client struct {
	rdb *redis.Client
}

func New(addr, password string, db int) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("keydb ping %s: %w", addr, err)
	}

	return &Client{rdb: rdb}, nil
}

func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return val, nil
}

func (c *Client) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, value, ttl).Err()
}

func (c *Client) Delete(ctx context.Context, key string) error {
	return c.rdb.Del(ctx, key).Err()
}

func (c *Client) Close() error {
	return c.rdb.Close()
}
