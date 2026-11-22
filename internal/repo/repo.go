// Package repo bundles one Store per resource type. Services take a *Repos
// rather than individual stores so wiring stays in one place.
//
// Each table belongs to one domain and lives in that domain's Postgres
// schema (see Tables). The fields below are grouped the same way. A
// domain's code uses only its own domain's stores and asks other domains
// through their interfaces (employer.API, network.API); a test enforces
// it. Demos, the simulator and seed loading use every store.
package repo

import (
	"context"

	"coreadmin/internal/store"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	customerv1 "coreadmin/gen/coreadmin/customer/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	planv1 "coreadmin/gen/coreadmin/plan/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	remittancev1 "coreadmin/gen/coreadmin/remittance/v1"
	stoplossv1 "coreadmin/gen/coreadmin/stoploss/v1"
)

type Repos struct {
	// DB runs transactions across all of the stores below.
	DB store.Transactor

	// Employer.
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

	// Claims.
	Claims store.Store[*claimsv1.Claim]
	// One record per subscriber unit, plan and benefit period.
	FamilyAccumulators store.Store[*eligibilityv1.FamilyAccumulators]
	FeeSchedules       store.Store[*providerv1.FeeSchedule]
	// 837 files received.
	ClaimIngestionJobs store.Store[*ingestionv1.IngestionJob]
	// 835s as sent.
	Remittances store.Store[*remittancev1.Remittance]
	// Claim number blocks.
	ClaimNumbers ClaimNumbers

	// Network.
	Providers store.Store[*providerv1.Provider]
	Networks  store.Store[*providerv1.Network]
	Contracts store.Store[*providerv1.Contract]

	// Finance.
	StopLossPolicies   store.Store[*stoplossv1.StopLossPolicy]
	SpecificExposures  store.Store[*stoplossv1.SpecificExposure]
	AggregateExposures store.Store[*stoplossv1.AggregateExposure]
	StopLossFilings    store.Store[*stoplossv1.StopLossFiling]
}

// InTx runs f in one transaction across every store: all of its writes
// commit together or none do. See store.Transactor.
func (r *Repos) InTx(ctx context.Context, f func(ctx context.Context) error) error {
	return r.DB.InTx(ctx, f)
}
