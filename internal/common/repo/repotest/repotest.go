// Package repotest gives each test an empty database of its own: a fresh
// schema on the shared local Postgres (see pgdb), dropped when the test
// ends.
package repotest

import (
	"context"
	"sync"
	"testing"
	"time"

	"coreadmin/internal/common/repo"
)

var once sync.Once

// New returns empty Repos for t.
func New(t testing.TB) *repo.Repos {
	t.Helper()
	ctx := context.Background()
	once.Do(func() { _ = repo.DropStaleSandboxes(ctx, time.Hour) })
	r, drop, err := repo.NewSandbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(drop)
	return r
}
