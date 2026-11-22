// Package repo bundles every domain's stores, for the code that sets up or
// spans every domain: demos, the simulator, seed loading, Journey. Services
// take a *Repos rather than individual stores so wiring stays in one
// place.
//
// Each domain declares its tables and stores in its own package
// (employerdb, claimsdb, networkdb, financedb), in its own Postgres schema;
// Repos embeds them, so repos.Members, repos.Claims and so on read as
// before. A domain's code uses only its own domain's stores and asks other
// domains through their services; a test enforces it.
package repo

import (
	"context"

	"coreadmin/internal/claims/claimsdb"
	"coreadmin/internal/common/store"
	"coreadmin/internal/employer/employerdb"
	"coreadmin/internal/finance/financedb"
	"coreadmin/internal/network/networkdb"
)

type Repos struct {
	// DB runs transactions across all of the stores below.
	DB store.Transactor

	employerdb.EmployerStores
	claimsdb.ClaimsStores
	networkdb.NetworkStores
	financedb.FinanceStores
}

// InTx runs f in one transaction across every store: all of its writes
// commit together or none do. See store.Transactor.
func (r *Repos) InTx(ctx context.Context, f func(ctx context.Context) error) error {
	return r.DB.InTx(ctx, f)
}
