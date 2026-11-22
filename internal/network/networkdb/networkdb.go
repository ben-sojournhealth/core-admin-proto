// Package networkdb declares the Network domain's tables, in its own
// Postgres schema, and the stores over them. Only Network code uses them;
// other domains ask NetworkService.
package networkdb

import (
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	"coreadmin/internal/common/store"
)

// Schema is the Network domain's Postgres schema.
const Schema = "network"

// Tables are the Network domain's tables, with the indexes their access
// patterns need.
var Tables = []store.Table{
	{Schema: Schema, Name: "providers", Indexes: []store.Index{
		{Name: "providers_by_npi", Path: []string{"npi"},
			Why: "Adjudication resolves the billing and rendering provider by NPI on every claim."},
	}},
	{Schema: Schema, Name: "networks"},
	{Schema: Schema, Name: "contracts"},
}

// NetworkStores are the stores over the Network domain's tables.
type NetworkStores struct {
	Providers store.Store[*providerv1.Provider]
	Networks  store.Store[*providerv1.Network]
	Contracts store.Store[*providerv1.Contract]
}

// New returns the stores in db, which must have Tables (store.PGDB.Migrate).
func New(db *store.PGDB) NetworkStores {
	return NetworkStores{
		Providers: store.NewPG[*providerv1.Provider](db, Schema, "providers"),
		Networks:  store.NewPG[*providerv1.Network](db, Schema, "networks"),
		Contracts: store.NewPG[*providerv1.Contract](db, Schema, "contracts"),
	}
}
