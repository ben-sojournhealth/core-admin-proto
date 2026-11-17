// Package repo bundles one Store per resource type. Services take a *Repos
// rather than individual stores so wiring stays in one place.
package repo

import (
	"coreadmin/internal/store"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	customerv1 "coreadmin/gen/coreadmin/customer/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	planv1 "coreadmin/gen/coreadmin/plan/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	stoplossv1 "coreadmin/gen/coreadmin/stoploss/v1"
)

type Repos struct {
	Customers store.Store[*customerv1.Customer]
	Groups    store.Store[*customerv1.Group]

	Plans store.Store[*planv1.Plan]

	Members store.Store[*eligibilityv1.Member]
	// One record per subscriber unit, plan and benefit period.
	FamilyAccumulators store.Store[*eligibilityv1.FamilyAccumulators]

	Providers    store.Store[*providerv1.Provider]
	Networks     store.Store[*providerv1.Network]
	Contracts    store.Store[*providerv1.Contract]
	FeeSchedules store.Store[*providerv1.FeeSchedule]

	Claims store.Store[*claimsv1.Claim]

	StopLossPolicies   store.Store[*stoplossv1.StopLossPolicy]
	SpecificExposures  store.Store[*stoplossv1.SpecificExposure]
	AggregateExposures store.Store[*stoplossv1.AggregateExposure]
	StopLossFilings    store.Store[*stoplossv1.StopLossFiling]

	IngestionJobs store.Store[*ingestionv1.IngestionJob]
}

// NewInMemory returns Repos backed entirely by store.Memory.
func NewInMemory() *Repos {
	return &Repos{
		Customers:          store.NewMemory[*customerv1.Customer](),
		Groups:             store.NewMemory[*customerv1.Group](),
		Plans:              store.NewMemory[*planv1.Plan](),
		Members:            store.NewMemory[*eligibilityv1.Member](),
		FamilyAccumulators: store.NewMemory[*eligibilityv1.FamilyAccumulators](),
		Providers:          store.NewMemory[*providerv1.Provider](),
		Networks:           store.NewMemory[*providerv1.Network](),
		Contracts:          store.NewMemory[*providerv1.Contract](),
		FeeSchedules:       store.NewMemory[*providerv1.FeeSchedule](),
		Claims:             store.NewMemory[*claimsv1.Claim](),
		StopLossPolicies:   store.NewMemory[*stoplossv1.StopLossPolicy](),
		SpecificExposures:  store.NewMemory[*stoplossv1.SpecificExposure](),
		AggregateExposures: store.NewMemory[*stoplossv1.AggregateExposure](),
		StopLossFilings:    store.NewMemory[*stoplossv1.StopLossFiling](),
		IngestionJobs:      store.NewMemory[*ingestionv1.IngestionJob](),
	}
}
