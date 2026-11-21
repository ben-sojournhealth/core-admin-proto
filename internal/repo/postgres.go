package repo

import (
	"context"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	customerv1 "coreadmin/gen/coreadmin/customer/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	planv1 "coreadmin/gen/coreadmin/plan/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	remittancev1 "coreadmin/gen/coreadmin/remittance/v1"
	stoplossv1 "coreadmin/gen/coreadmin/stoploss/v1"
	"coreadmin/internal/store"
)

// Tables lists every table, with the indexes its access patterns need.
// An index exists for each lookup that would otherwise read the whole
// table; the code asks for it with store.Eq, store.Prefix or
// store.Contains on the same path. Lookups by id use the primary key.
// Tables with no indexes are read by id, or whole (they are small, or
// always read in full).
var Tables = []store.Table{
	{Name: "customers"},
	{Name: "groups", Indexes: []store.Index{
		{Name: "groups_by_customer", Path: []string{"customerId"},
			Why: "Eligibility resolves an 834 coverage loop to the plan of the customer's group."},
	}},
	{Name: "plans"},
	{Name: "members", Indexes: []store.Index{
		{Name: "members_by_card", Path: []string{"cardId"},
			Why: "Adjudication finds the patient by the member id on the claim; every claim does this."},
		{Name: "members_by_subscriber", Path: []string{"subscriberId"},
			Why: "A family: matching a dependent sent under the subscriber's card, and the accumulators view."},
		{Name: "members_by_customer", Path: []string{"customerId"},
			Why: "A customer's members: full-file reconciliation and eligibility views."},
	}},
	{Name: "family_accumulators"},
	{Name: "providers", Indexes: []store.Index{
		{Name: "providers_by_npi", Path: []string{"npi"},
			Why: "Adjudication resolves the billing and rendering provider by NPI on every claim."},
	}},
	{Name: "networks"},
	{Name: "contracts"},
	{Name: "fee_schedules"},
	{Name: "claims", Indexes: []store.Index{
		{Name: "claims_by_number", Path: []string{"claimNumber"},
			Why: "Claim numbers: the next number of the day (by prefix), the versions of a number, and resolving a number a provider or examiner quotes."},
		{Name: "claims_by_original", Path: []string{"originalClaimId"},
			Why: "A claim's replacements and voids: the replacement-in-progress check on every correction, and lineage."},
		{Name: "claims_by_member", Path: []string{"memberId"},
			Why: "The duplicate check on every claim compares it with the member's other claims."},
		{Name: "claims_by_family", Path: []string{"adjudication", "familyAccumulatorsId"},
			Why: "When a claim's accumulator postings change, the family's later claims are flagged for review, and found again by the flags they carry."},
		{Name: "claims_by_payment_state", Path: []string{"payment", "state"},
			Why: "Releasing the payment run reads the claims selected for it."},
		{Name: "claims_by_ingestion_job", Path: []string{"sourceRecord", "ingestionJobId"},
			Why: "The claims an 837 file loaded, for the intake view."},
	}},
	{Name: "stop_loss_policies"},
	{Name: "specific_exposures"},
	{Name: "aggregate_exposures"},
	{Name: "stop_loss_filings"},
	{Name: "ingestion_jobs", Indexes: []store.Index{
		{Name: "ingestion_jobs_by_icn", Path: []string{"envelope", "interchangeControlNumber"},
			Why: "The duplicate-file check on every inbound file (sender and interchange control number)."},
	}},
	{Name: "enrollment_transactions", Indexes: []store.Index{
		{Name: "enrollment_transactions_by_job", Path: []string{"source", "ingestionJobId"},
			Why: "The member loops an 834 file loaded, for the intake view."},
	}},
	// Always read whole, in sequence (id) order: replay is the access
	// pattern, and the primary key serves it.
	{Name: "eligibility_inputs"},
	{Name: "member_versions", Indexes: []store.Index{
		{Name: "member_versions_by_member", Path: []string{"memberId"},
			Why: "A member's history: as-of questions, the version chart, and numbering the next version."},
	}},
	{Name: "remittances", Indexes: []store.Index{
		{Name: "remittances_doc", GIN: true,
			Why: "A replacement's reversal needs the 835 that reported the original (claims contains its claimId)."},
	}},
}

// NewPostgres returns Repos backed by Postgres, in db's schema, creating
// the tables and indexes if they're missing.
func NewPostgres(ctx context.Context, db *store.PGDB) (*Repos, error) {
	if err := db.Migrate(ctx, Tables); err != nil {
		return nil, err
	}
	numbers := &pgClaimNumbers{db: db}
	if err := numbers.migrate(ctx); err != nil {
		return nil, err
	}
	return &Repos{
		ClaimNumbers:           numbers,
		DB:                     db,
		Customers:              store.NewPG[*customerv1.Customer](db, "customers"),
		Groups:                 store.NewPG[*customerv1.Group](db, "groups"),
		Plans:                  store.NewPG[*planv1.Plan](db, "plans"),
		Members:                store.NewPG[*eligibilityv1.Member](db, "members"),
		FamilyAccumulators:     store.NewPG[*eligibilityv1.FamilyAccumulators](db, "family_accumulators"),
		EnrollmentTransactions: store.NewPG[*eligibilityv1.EnrollmentTransaction](db, "enrollment_transactions"),
		EligibilityInputs:      store.NewPG[*eligibilityv1.EligibilityInput](db, "eligibility_inputs"),
		MemberVersions:         store.NewPG[*eligibilityv1.MemberVersion](db, "member_versions"),
		Providers:              store.NewPG[*providerv1.Provider](db, "providers"),
		Networks:               store.NewPG[*providerv1.Network](db, "networks"),
		Contracts:              store.NewPG[*providerv1.Contract](db, "contracts"),
		FeeSchedules:           store.NewPG[*providerv1.FeeSchedule](db, "fee_schedules"),
		Claims:                 store.NewPG[*claimsv1.Claim](db, "claims"),
		StopLossPolicies:       store.NewPG[*stoplossv1.StopLossPolicy](db, "stop_loss_policies"),
		SpecificExposures:      store.NewPG[*stoplossv1.SpecificExposure](db, "specific_exposures"),
		AggregateExposures:     store.NewPG[*stoplossv1.AggregateExposure](db, "aggregate_exposures"),
		StopLossFilings:        store.NewPG[*stoplossv1.StopLossFiling](db, "stop_loss_filings"),
		IngestionJobs:          store.NewPG[*ingestionv1.IngestionJob](db, "ingestion_jobs"),
		Remittances:            store.NewPG[*remittancev1.Remittance](db, "remittances"),
	}, nil
}
