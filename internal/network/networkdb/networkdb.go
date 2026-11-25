// Package networkdb declares the Network domain's tables, in its own
// Postgres schema, and the stores over them. Only Network code uses them;
// other domains ask NetworkService.
package networkdb

import (
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
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
	{Schema: Schema, Name: "participations", Indexes: []store.Index{
		{Name: "participations_by_npi", Path: []string{"npi"},
			Why: "A provider's participations: network status on every claim, and the TINs an NPI rolls up to."},
		{Name: "participations_by_tin", Path: []string{"tin"},
			Why: "The providers under a TIN: the roll-up read from the TIN's side."},
		{Name: "participations_by_source", Path: []string{"sourceId"},
			Why: "What a source states today, compared with its next full file."},
	}},
	{Schema: Schema, Name: "networks"},
	{Schema: Schema, Name: "underlying_networks"},
	{Schema: Schema, Name: "contracts"},
	// Vendor provider files received.
	{Schema: Schema, Name: "ingestion_jobs"},
	{Schema: Schema, Name: "provider_updates", Indexes: []store.Index{
		{Name: "provider_updates_by_source", Path: []string{"sourceId"},
			Why: "A source's updates: whether one is awaiting a decision before its next file is taken."},
	}},
	{Schema: Schema, Name: "vendor_provider_changes", Indexes: []store.Index{
		{Name: "vendor_provider_changes_by_update", Path: []string{"providerUpdateId"},
			Why: "An update's changes: the review, and applying them on approval."},
	}},
}

// NetworkStores are the stores over the Network domain's tables.
type NetworkStores struct {
	// Who a provider is, one per NPI.
	Providers store.Store[*providerv1.Provider]
	// A provider, under a TIN, in an underlying network: the NPI to TIN
	// roll-up.
	Participations store.Store[*providerv1.NetworkParticipation]
	// Networks are what plans opt into; UnderlyingNetworks are what
	// providers participate in.
	Networks           store.Store[*providerv1.Network]
	UnderlyingNetworks store.Store[*providerv1.UnderlyingNetwork]
	Contracts          store.Store[*providerv1.Contract]
	// Vendor provider files received.
	VendorIngestionJobs store.Store[*ingestionv1.IngestionJob]
	// What each file would change, approved or rejected as a whole.
	ProviderUpdates store.Store[*providerv1.ProviderUpdate]
	// An update's changes, one per NPI and TIN that differs.
	VendorProviderChanges store.Store[*providerv1.VendorProviderChange]
}

// New returns the stores in db, which must have Tables (store.PGDB.Migrate).
func New(db *store.PGDB) NetworkStores {
	return NetworkStores{
		Providers:             store.NewPG[*providerv1.Provider](db, Schema, "providers"),
		Participations:        store.NewPG[*providerv1.NetworkParticipation](db, Schema, "participations"),
		Networks:              store.NewPG[*providerv1.Network](db, Schema, "networks"),
		UnderlyingNetworks:    store.NewPG[*providerv1.UnderlyingNetwork](db, Schema, "underlying_networks"),
		Contracts:             store.NewPG[*providerv1.Contract](db, Schema, "contracts"),
		VendorIngestionJobs:   store.NewPG[*ingestionv1.IngestionJob](db, Schema, "ingestion_jobs"),
		ProviderUpdates:       store.NewPG[*providerv1.ProviderUpdate](db, Schema, "provider_updates"),
		VendorProviderChanges: store.NewPG[*providerv1.VendorProviderChange](db, Schema, "vendor_provider_changes"),
	}
}
