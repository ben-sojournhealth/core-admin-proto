package repo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"coreadmin/internal/pgdb"
	"coreadmin/internal/store"
)

// Open returns Repos in a named sandbox of this process's database: a
// schema per domain, <sandbox>_<domain>, created if needed. With fresh, the
// sandbox's schemas are dropped and recreated first: the demo's long-lived
// sandboxes start over this way.
func Open(ctx context.Context, sandbox string, fresh bool) (*Repos, error) {
	pool, err := pgdb.Pool(ctx)
	if err != nil {
		return nil, err
	}
	db := &store.PGDB{Pool: pool, Prefix: sandbox}
	if fresh {
		if err := db.Drop(ctx, Domains...); err != nil {
			return nil, err
		}
	}
	return NewPostgres(ctx, db)
}

// SandboxPrefix starts the name of every throwaway sandbox, followed by
// when it was made, so leftovers can be found and dropped.
const SandboxPrefix = "s_"

// NewSandbox returns Repos in a new, empty sandbox of their own, and a
// function that drops it. For per-request sandboxes and tests.
func NewSandbox(ctx context.Context) (*Repos, func(), error) {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	sandbox := fmt.Sprintf("%s%d_%s", SandboxPrefix, time.Now().Unix(), hex.EncodeToString(b))
	r, err := Open(ctx, sandbox, false)
	if err != nil {
		return nil, nil, err
	}
	drop := func() { _ = r.DB.(*store.PGDB).Drop(context.WithoutCancel(ctx), Domains...) }
	return r, drop, nil
}

// DropStaleSandboxes drops throwaway sandboxes' schemas older than age:
// ones a crashed test or request never dropped.
func DropStaleSandboxes(ctx context.Context, age time.Duration) error {
	pool, err := pgdb.Pool(ctx)
	if err != nil {
		return err
	}
	rows, err := pool.Query(ctx, `SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE 's\_%'`)
	if err != nil {
		return err
	}
	var stale []string
	cutoff := time.Now().Add(-age).Unix()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		var at int64
		var rest string
		if _, err := fmt.Sscanf(name, SandboxPrefix+"%d_%s", &at, &rest); err == nil && at < cutoff {
			stale = append(stale, name)
		}
	}
	rows.Close()
	for _, name := range stale {
		_ = store.DropSchema(ctx, pool, name)
	}
	return rows.Err()
}
