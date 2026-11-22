package repo

import (
	"context"
	"slices"

	"coreadmin/internal/claims/claimsdb"
	"coreadmin/internal/common/store"
	"coreadmin/internal/employer/employerdb"
	"coreadmin/internal/finance/financedb"
	"coreadmin/internal/network/networkdb"
)

// Domains, each with a Postgres schema of its own that holds its tables.
// A domain reads and writes only its own schema. Each domain declares its
// tables and stores in its own <domain>db package.
const (
	Employer = employerdb.Schema
	Claims   = claimsdb.Schema
	Network  = networkdb.Schema
	Finance  = financedb.Schema
)

// Domains lists every domain that has a schema.
var Domains = []string{Employer, Claims, Network, Finance}

// Tables lists every domain's tables.
var Tables = slices.Concat(employerdb.Tables, claimsdb.Tables, networkdb.Tables, financedb.Tables)

// NewPostgres returns Repos backed by Postgres, in db's domain schemas,
// creating the tables and indexes if they're missing.
func NewPostgres(ctx context.Context, db *store.PGDB) (*Repos, error) {
	if err := db.Migrate(ctx, Tables); err != nil {
		return nil, err
	}
	claims, err := claimsdb.New(ctx, db)
	if err != nil {
		return nil, err
	}
	return &Repos{
		DB:             db,
		EmployerStores: employerdb.New(db),
		ClaimsStores:   claims,
		NetworkStores:  networkdb.New(db),
		FinanceStores:  financedb.New(db),
	}, nil
}
