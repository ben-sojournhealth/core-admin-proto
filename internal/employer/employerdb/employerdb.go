// Package employerdb declares the Employer domain's tables, in its own
// Postgres schema, and the stores over them. Only Employer code uses them;
// other domains ask EmployerService.
package employerdb

import (
	customerv1 "coreadmin/gen/coreadmin/customer/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	planv1 "coreadmin/gen/coreadmin/plan/v1"
	"coreadmin/internal/common/store"
)

// Schema is the Employer domain's Postgres schema.
const Schema = "employer"

// Tables are the Employer domain's tables, with the indexes their access
// patterns need. An index exists for each lookup that would otherwise read
// the whole table; the code asks for it with store.Eq, store.Prefix or
// store.Contains on the same path.
var Tables = []store.Table{
	{Schema: Schema, Name: "customers"},
	{Schema: Schema, Name: "groups", Indexes: []store.Index{
		{Name: "groups_by_customer", Path: []string{"customerId"},
			Why: "Eligibility resolves an 834 coverage loop to the plan of the customer's group."},
	}},
	{Schema: Schema, Name: "plans"},
	{Schema: Schema, Name: "members", Indexes: []store.Index{
		{Name: "members_by_card", Path: []string{"cardId"},
			Why: "Adjudication finds the patient by the member id on the claim; every claim does this."},
		{Name: "members_by_subscriber", Path: []string{"subscriberId"},
			Why: "A family: matching a dependent sent under the subscriber's card, and the accumulators view."},
		{Name: "members_by_customer", Path: []string{"customerId"},
			Why: "A customer's members: full-file reconciliation and eligibility views."},
	}},
	// 834 files received.
	{Schema: Schema, Name: "ingestion_jobs", Indexes: []store.Index{
		{Name: "ingestion_jobs_by_icn", Path: []string{"envelope", "interchangeControlNumber"},
			Why: "The duplicate-file check on every inbound 834 (sender and interchange control number)."},
	}},
	{Schema: Schema, Name: "enrollment_transactions", Indexes: []store.Index{
		{Name: "enrollment_transactions_by_job", Path: []string{"source", "ingestionJobId"},
			Why: "The member loops an 834 file loaded, for the intake view."},
	}},
	// Always read whole, in sequence (id) order: replay is the access
	// pattern, and the primary key serves it.
	{Schema: Schema, Name: "eligibility_inputs"},
	{Schema: Schema, Name: "member_versions", Indexes: []store.Index{
		{Name: "member_versions_by_member", Path: []string{"memberId"},
			Why: "A member's history: as-of questions, the version chart, and numbering the next version."},
	}},
}

// EmployerStores are the stores over the Employer domain's tables.
type EmployerStores struct {
	Customers store.Store[*customerv1.Customer]
	Groups    store.Store[*customerv1.Group]
	Plans     store.Store[*planv1.Plan]
	Members   store.Store[*eligibilityv1.Member]
	// 834 files received.
	EnrollmentIngestionJobs store.Store[*ingestionv1.IngestionJob]
	// 834 member loops as sent.
	EnrollmentTransactions store.Store[*eligibilityv1.EnrollmentTransaction]
	// Everything that can change eligibility, in order; append-only.
	EligibilityInputs store.Store[*eligibilityv1.EligibilityInput]
	// Every version of every member eligibility derived; append-only.
	MemberVersions store.Store[*eligibilityv1.MemberVersion]
}

// New returns the stores in db, which must have Tables (store.PGDB.Migrate).
func New(db *store.PGDB) EmployerStores {
	return EmployerStores{
		Customers:               store.NewPG[*customerv1.Customer](db, Schema, "customers"),
		Groups:                  store.NewPG[*customerv1.Group](db, Schema, "groups"),
		Plans:                   store.NewPG[*planv1.Plan](db, Schema, "plans"),
		Members:                 store.NewPG[*eligibilityv1.Member](db, Schema, "members"),
		EnrollmentIngestionJobs: store.NewPG[*ingestionv1.IngestionJob](db, Schema, "ingestion_jobs"),
		EnrollmentTransactions:  store.NewPG[*eligibilityv1.EnrollmentTransaction](db, Schema, "enrollment_transactions"),
		EligibilityInputs:       store.NewPG[*eligibilityv1.EligibilityInput](db, Schema, "eligibility_inputs"),
		MemberVersions:          store.NewPG[*eligibilityv1.MemberVersion](db, Schema, "member_versions"),
	}
}
