package adjudication

// Step is one thing adjudication does, for the adjudication map: what it
// decides, the pends and denials it can end in, what it asks other
// domains, and the Claims data it reads or writes. Steps are listed, not
// ordered: their order and dependencies are still to be designed, and
// some (pricing rules) apply to the whole claim as well as to lines.
type Step struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Sub    string   `json:"sub"`
	Pends  []string `json:"pends,omitempty"`
	Denies []string `json:"denies,omitempty"`
	// Calls to other domains' services.
	Calls []StepCall `json:"calls,omitempty"`
	// Claims-domain data it uses.
	Data []string `json:"data,omitempty"`
}

// StepCall is the methods a step calls on another domain's service.
type StepCall struct {
	Service string   `json:"service"`
	Methods []string `json:"methods"`
}

const (
	employerService = "coreadmin.employer.v1.EmployerService"
	networkService  = "coreadmin.network.v1.NetworkService"
)

// Steps lists what the engine does. The list implies no order.
var Steps = []Step{
	{ID: "match-original", Title: "Match original", Sub: "replacements & voids · locks it",
		Pends: []string{PendOriginalNotFound, PendOriginalNotFinalized, PendOriginalInPaymentRun, PendOpenReplacement},
		Data:  []string{"claims"}},
	{ID: "match-member", Title: "Match member", Sub: "member id · dependent by name & birth date",
		Pends: []string{PendMemberNotFound},
		Calls: []StepCall{{Service: employerService, Methods: []string{"MatchClaimMember"}}}},
	{ID: "eligibility", Title: "Eligibility", Sub: "coverage on the date of service",
		Pends:  []string{PendNoEligibility, PendCoverageInGrace},
		Denies: []string{DenyCoverageTerminated},
		Calls:  []StepCall{{Service: employerService, Methods: []string{"GetMedicalCoverage"}}}},
	{ID: "plan", Title: "Plan version", Sub: "in force on the date of service",
		Pends: []string{PendNoPlanVersion},
		Calls: []StepCall{{Service: employerService, Methods: []string{"GetPlanVersion"}}}},
	{ID: "duplicate", Title: "Duplicate check", Sub: "member · provider · date · services",
		Pends: []string{PendPossibleDuplicate},
		Data:  []string{"claims"}},
	{ID: "provider", Title: "Provider & tier", Sub: "participation on the date of service",
		Pends:  []string{PendNoTier},
		Denies: []string{DenyProviderSanctioned},
		Calls:  []StepCall{{Service: networkService, Methods: []string{"GetProvider"}}}},
	{ID: "snapshot", Title: "Accumulators", Sub: "the family's totals so far",
		Data: []string{"family_accumulators"}},
	{ID: "benefit", Title: "Benefit", Sub: "service category · tier cost share",
		Pends:  []string{PendNoBenefitMatch},
		Denies: []string{DenyNotCovered}},
	{ID: "auth", Title: "Prior auth", Sub: "when the benefit requires it",
		Pends: []string{PendPriorAuthRequired}},
	{ID: "price", Title: "Price", Sub: "contract · fee schedule · non-contracted",
		Pends: []string{PendNoPrice},
		Calls: []StepCall{{Service: networkService, Methods: []string{"GetContract"}}},
		Data:  []string{"fee_schedules"}},
	{ID: "cost-share", Title: "Cost share", Sub: "deductible · copay · coinsurance · OOP",
		Data: []string{"family_accumulators"}},
	{ID: "cas", Title: "CAS & balance", Sub: "adjustment codes · the line balances"},
	{ID: "remarks", Title: "Remark codes", Sub: "each CARC with a required RARC",
		Pends: []string{PendRemarkCodeRequired}},
	{ID: "outcome", Title: "Outcome", Sub: "pended · approved · denied · totals"},
}
