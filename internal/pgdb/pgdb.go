// Package pgdb connects to the local Postgres everything runs on: the
// demo, the tests and the per-request sandboxes.
//
// One Postgres serves them all: an embedded one, with its data under
// ./data/pg at the module root, on port 5433. `make pg` starts it; if it
// isn't running, the first caller starts it, and it keeps running after
// that process exits, so later runs reuse it. COREADMIN_PG_URL points at a
// different server instead.
//
// The demo uses the database coreadmin; tests use coreadmin_test, so a
// test can never touch demo data.
package pgdb

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Port is the embedded Postgres's port.
const Port = 5433

var (
	mu    sync.Mutex
	pools = map[string]*pgxpool.Pool{}
)

// Database is the database this process uses: coreadmin_test in a test
// binary, coreadmin otherwise.
func Database() string {
	if testing.Testing() {
		return "coreadmin_test"
	}
	return "coreadmin"
}

// Pool returns this process's pool for Database, starting Postgres and
// creating the database if needed.
func Pool(ctx context.Context) (*pgxpool.Pool, error) {
	mu.Lock()
	defer mu.Unlock()
	name := Database()
	if p := pools[name]; p != nil {
		return p, nil
	}
	server, err := ensureServer(ctx)
	if err != nil {
		return nil, err
	}
	if err := ensureDatabase(ctx, server, name); err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(server + name)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 16
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pools[name] = p
	return p, nil
}

// URL is how to connect to a database on the server, e.g. for psql.
func URL(database string) string { return serverURL() + database }

func serverURL() string {
	if u := os.Getenv("COREADMIN_PG_URL"); u != "" {
		return u
	}
	return fmt.Sprintf("postgres://postgres:postgres@localhost:%d/", Port)
}

// ensureServer returns the server URL (ending in "/"), starting the
// embedded Postgres if nothing answers.
func ensureServer(ctx context.Context) (string, error) {
	url := serverURL()
	if up(ctx, url) {
		return url, nil
	}
	if os.Getenv("COREADMIN_PG_URL") != "" {
		return "", fmt.Errorf("pgdb: no Postgres at COREADMIN_PG_URL")
	}
	if err := Start(); err != nil {
		// Another process may have started it at the same moment.
		for range 30 {
			if up(ctx, url) {
				return url, nil
			}
			time.Sleep(500 * time.Millisecond)
		}
		return "", err
	}
	return url, nil
}

func up(ctx context.Context, url string) bool {
	if os.Getenv("COREADMIN_PG_URL") == "" {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", Port), 300*time.Millisecond)
		if err != nil {
			return false
		}
		c.Close()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url+"postgres")
	if err != nil {
		return false
	}
	conn.Close(ctx)
	return true
}

// Dir is where the embedded Postgres keeps its data and binaries.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	// internal/pgdb/pgdb.go -> module root.
	return filepath.Join(filepath.Dir(file), "..", "..", "data", "pg")
}

// Start starts the embedded Postgres, leaving it running. It downloads
// Postgres the first time.
func Start() error {
	dir := Dir()
	db := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().Port(Port).Version(embeddedpostgres.V16).
		DataPath(filepath.Join(dir, "data")).RuntimePath(filepath.Join(dir, "run")).
		BinariesPath(filepath.Join(dir, "bin")).Logger(nil).StartTimeout(60 * time.Second))
	if err := db.Start(); err != nil {
		return fmt.Errorf("pgdb: start embedded Postgres: %w", err)
	}
	return nil
}

func ensureDatabase(ctx context.Context, server, name string) error {
	conn, err := pgx.Connect(ctx, server+"postgres")
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
			// Created by another process meanwhile.
			var again bool
			if conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&again); !again {
				return err
			}
		}
	}
	return nil
}
