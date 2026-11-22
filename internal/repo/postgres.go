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

// Domains, each with a Postgres schema of its own that holds its tables.
// A domain reads and writes only its own schema.
const (
	Employer = "employer"
	Claims   = "claims"
	Network  = "network"
	Finance  = "finance"
)

// Domains lists every domain that has a schema.
var Domains = []string{Employer, Claims, Network, Finance}

// Tables lists every table, by the domain that owns it, with the indexes
// its access patterns need.
// An index exists for each lookup that would otherwise read the whole
// table; the code asks for it with store.Eq, store.Prefix or
// store.Contains on the same path. Lookups by id use the primary key.
// Tables with no indexes are read by id, or whole (they are small, or
// always read in full).
var Tables = []store.Table{
	// Employer: customers and their groups, plan design, enrollment and
	// eligibility.
	{Schema: Employer, Name: "customers"},
	{Schema: Employer, Name: "groups", Indexes: []store.Index{
		{Name: "groups_by_customer", Path: []string{"customerId"},
			Why: "Eligibility resolves an 834 coverage loop to the plan of the customer's group."},
	}},
	{Schema: Employer, Name: "plans"},
	{Schema: Employer, Name: "members", Indexes: []store.Index{
		{Name: "members_by_card", Path: []string{"cardId"},
			Why: "Adjudication finds the patient by the member id on the claim; every claim does this."},
		{Name: "members_by_subscriber", Path: []string{"subscriberId"},
			Why: "A family: matching a dependent sent under the subscriber's card, and the accumulators view."},
		{Name: "members_by_customer", Path: []string{"customerId"},
			Why: "A customer's members: full-file reconciliation and eligibility views."},
	}},
	// 834 files received.
	{Schema: Employer, Name: "ingestion_jobs", Indexes: []store.Index{
		{Name: "ingestion_jobs_by_icn", Path: []string{"envelope", "interchangeControlNumber"},
			Why: "The duplicate-file check on every inbound 834 (sender and interchange control number)."},
	}},
	{Schema: Employer, Name: "enrollment_transactions", Indexes: []store.Index{
		{Name: "enrollment_transactions_by_job", Path: []string{"source", "ingestionJobId"},
			Why: "The member loops an 834 file loaded, for the intake view."},
	}},
	// Always read whole, in sequence (id) order: replay is the access
	// pattern, and the primary key serves it.
	{Schema: Employer, Name: "eligibility_inputs"},
	{Schema: Employer, Name: "member_versions", Indexes: []store.Index{
		{Name: "member_versions_by_member", Path: []string{"memberId"},
			Why: "A member's history: as-of questions, the version chart, and numbering the next version."},
	}},

	// Claims: claims, their accumulators, pricing, 837 intake and 835s.
	// (Claim number blocks are a table of their own; see numbers.go.)
	{Schema: Claims, Name: "claims", Indexes: []store.Index{
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
	{Schema: Claims, Name: "family_accumulators"},
	{Schema: Claims, Name: "fee_schedules"},
	// 837 files received.
	{Schema: Claims, Name: "ingestion_jobs", Indexes: []store.Index{
		{Name: "ingestion_jobs_by_icn", Path: []string{"envelope", "interchangeControlNumber"},
			Why: "The duplicate-file check on every inbound 837 (sender and interchange control number)."},
	}},
	{Schema: Claims, Name: "remittances", Indexes: []store.Index{
		{Name: "remittances_doc", GIN: true,
			Why: "A replacement's reversal needs the 835 that reported the original (claims contains its claimId)."},
	}},

	// Network: providers, networks and contracts.
	{Schema: Network, Name: "providers", Indexes: []store.Index{
		{Name: "providers_by_npi", Path: []string{"npi"},
			Why: "Adjudication resolves the billing and rendering provider by NPI on every claim."},
	}},
	{Schema: Network, Name: "networks"},
	{Schema: Network, Name: "contracts"},

	// Finance: stop-loss.
	{Schema: Finance, Name: "stop_loss_policies"},
	{Schema: Finance, Name: "specific_exposures"},
	{Schema: Finance, Name: "aggregate_exposures"},
	{Schema: Finance, Name: "stop_loss_filings"},
}

// NewPostgres returns Repos backed by Postgres, in db's domain schemas,
// creating the tables and indexes if they're missing.
func NewPostgres(ctx context.Context, db *store.PGDB) (*Repos, error) {
	if err := db.Migrate(ctx, Tables); err != nil {
		return nil, err
	}
	numbers := &pgClaimNumbers{db: db}
	if err := numbers.migrate(ctx); err != nil {
		return nil, err
	}
	return &Repos{
		ClaimNumbers: numbers,
		DB:           db,

		Customers:               store.NewPG[*customerv1.Customer](db, Employer, "customers"),
		Groups:                  store.NewPG[*customerv1.Group](db, Employer, "groups"),
		Plans:                   store.NewPG[*planv1.Plan](db, Employer, "plans"),
		Members:                 store.NewPG[*eligibilityv1.Member](db, Employer, "members"),
		EnrollmentIngestionJobs: store.NewPG[*ingestionv1.IngestionJob](db, Employer, "ingestion_jobs"),
		EnrollmentTransactions:  store.NewPG[*eligibilityv1.EnrollmentTransaction](db, Employer, "enrollment_transactions"),
		EligibilityInputs:       store.NewPG[*eligibilityv1.EligibilityInput](db, Employer, "eligibility_inputs"),
		MemberVersions:          store.NewPG[*eligibilityv1.MemberVersion](db, Employer, "member_versions"),

		Claims:             store.NewPG[*claimsv1.Claim](db, Claims, "claims"),
		FamilyAccumulators: store.NewPG[*eligibilityv1.FamilyAccumulators](db, Claims, "family_accumulators"),
		FeeSchedules:       store.NewPG[*providerv1.FeeSchedule](db, Claims, "fee_schedules"),
		ClaimIngestionJobs: store.NewPG[*ingestionv1.IngestionJob](db, Claims, "ingestion_jobs"),
		Remittances:        store.NewPG[*remittancev1.Remittance](db, Claims, "remittances"),

		Providers: store.NewPG[*providerv1.Provider](db, Network, "providers"),
		Networks:  store.NewPG[*providerv1.Network](db, Network, "networks"),
		Contracts: store.NewPG[*providerv1.Contract](db, Network, "contracts"),

		StopLossPolicies:   store.NewPG[*stoplossv1.StopLossPolicy](db, Finance, "stop_loss_policies"),
		SpecificExposures:  store.NewPG[*stoplossv1.SpecificExposure](db, Finance, "specific_exposures"),
		AggregateExposures: store.NewPG[*stoplossv1.AggregateExposure](db, Finance, "aggregate_exposures"),
		StopLossFilings:    store.NewPG[*stoplossv1.StopLossFiling](db, Finance, "stop_loss_filings"),
	}, nil
}
