package database

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool creates and verifies a PostgreSQL connection pool.
//
// Serverless Postgres providers (Neon, etc.) suspend their compute after a
// period of inactivity and take a few seconds to wake back up on the next
// connection. A single fast ping can time out mid-wakeup, so we retry a
// few times with a generous per-attempt timeout before giving up.
func NewPool(databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing database url: %w", err)
	}

	cfg.MaxConns = 10
	cfg.MinConns = 2

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	const maxAttempts = 5
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := pool.Ping(ctx)
		cancel()

		if err == nil {
			return pool, nil
		}

		lastErr = err
		log.Printf("database ping attempt %d/%d failed: %v (retrying...)", attempt, maxAttempts, err)
		time.Sleep(2 * time.Second)
	}

	pool.Close()
	return nil, fmt.Errorf("pinging database after %d attempts: %w", maxAttempts, lastErr)
}