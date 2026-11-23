// Package claimsdb declares the Claims domain's tables, in its own
// Postgres schema, and the stores over them. Only Claims code uses them.
package claimsdb

import (
	"context"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	remittancev1 "coreadmin/gen/coreadmin/remittance/v1"
	"coreadmin/internal/common/store"
)

// Schema is the Claims domain's Postgres schema.
const Schema = "claims"

// Tables are the Claims domain's tables, with the indexes their access
// patterns need. Claim number blocks are a table of their own (see
// numbers.go).
var Tables = []store.Table{
	{Schema: Schema, Name: "claims", Indexes: []store.Index{
		{Name: "claims_by_number", Path: []string{"claimNumber"},
			Why: "Claim numbers: the next number of the day (by prefix), the versions of a number, and resolving a number a provider or examiner quotes."},
		{Name: "claims_by_original", Path: []string{"originalClaimId"},
			Why: "A claim's replacements and voids: the replacement-in-progress check on every correction, and lineage."},
		{Name: "claims_by_member", Path: []string{"memberId"},
			Why: "The duplicate check on every claim compares it with the member's other claims."},
		{Name: "claims_by_payment_state", Path: []string{"payment", "state"},
			Why: "Releasing the payment run reads the claims selected for it."},
		{Name: "claims_by_ingestion_job", Path: []string{"sourceRecord", "ingestionJobId"},
			Why: "The claims an 837 file loaded, for the intake view."},
	}},
	{Schema: Schema, Name: "family_accumulators"},
	{Schema: Schema, Name: "fee_schedules"},
	// 837 files received.
	{Schema: Schema, Name: "ingestion_jobs", Indexes: []store.Index{
		{Name: "ingestion_jobs_by_icn", Path: []string{"envelope", "interchangeControlNumber"},
			Why: "The duplicate-file check on every inbound 837 (sender and interchange control number)."},
	}},
	{Schema: Schema, Name: "remittances", Indexes: []store.Index{
		{Name: "remittances_doc", GIN: true,
			Why: "A replacement's reversal needs the 835 that reported the original (claims contains its claimId)."},
	}},
}

// ClaimsStores are the stores over the Claims domain's tables.
type ClaimsStores struct {
	Claims store.Store[*claimsv1.Claim]
	// One record per subscriber unit, plan and benefit period.
	FamilyAccumulators store.Store[*claimsv1.FamilyAccumulators]
	FeeSchedules       store.Store[*providerv1.FeeSchedule]
	// 837 files received.
	ClaimIngestionJobs store.Store[*ingestionv1.IngestionJob]
	// 835s as sent.
	Remittances store.Store[*remittancev1.Remittance]
	// Claim number blocks.
	ClaimNumbers ClaimNumbers
}

// New returns the stores in db, which must have Tables (store.PGDB.Migrate),
// creating the claim number blocks table if it's missing.
func New(ctx context.Context, db *store.PGDB) (ClaimsStores, error) {
	numbers := &pgClaimNumbers{db: db}
	if err := numbers.migrate(ctx); err != nil {
		return ClaimsStores{}, err
	}
	return ClaimsStores{
		Claims:             store.NewPG[*claimsv1.Claim](db, Schema, "claims"),
		FamilyAccumulators: store.NewPG[*claimsv1.FamilyAccumulators](db, Schema, "family_accumulators"),
		FeeSchedules:       store.NewPG[*providerv1.FeeSchedule](db, Schema, "fee_schedules"),
		ClaimIngestionJobs: store.NewPG[*ingestionv1.IngestionJob](db, Schema, "ingestion_jobs"),
		Remittances:        store.NewPG[*remittancev1.Remittance](db, Schema, "remittances"),
		ClaimNumbers:       numbers,
	}, nil
}
