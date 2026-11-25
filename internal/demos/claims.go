package demos

import "coreadmin/internal/scenario"

// Claims components.
const (
	Clearinghouse = "clearinghouse" // 837 claims
	ClaimsIntake  = "claims-intake" // 837, 999
	// Named groups of procedure codes (burn treatment, blood transfusion)
	// that plan design names and adjudication's rules refer to.
	ServiceDefinitions = "service-definitions"
	Adjudication       = "adjudication" // pricing, cost share, accumulators, pends, adjustments, voids
	// 835s, provider EOPs and member EOBs: built from the adjudication,
	// viewable before the claim is adjudicated; payment details fill in once
	// it pays.
	Remittance = "remittance"
	// What each claim pays, to whom and from whose money: a pure function of
	// the claim, previewable before it is adjudicated; the payment cycle nets
	// recorded lines across claims.
	Settlement = "settlement"
	// What a payee holds beyond what a claim now owes them, after an
	// adjustment or void of a reported claim: payee, customer, amount, reason.
	Overpayments = "overpayments"
	// Whoever adjudicates and pays a benefit's claims instead of us: the
	// PBM, and behavioral, dental and vision vendors.
	ExternalProcessors = "external-processors"
	// Claims someone else adjudicates and pays. One component on the map's
	// overview; inside the Claims domain it is its two halves.
	ExternalClaims = "external-claims"
	// Their paid claims, stored as sent and never adjudicated by us.
	ExternalClaimsIntake = "external-claims-intake"
	// What each side applied to a shared accumulator: ours sent out, theirs
	// posted to the accumulator ledger.
	AccumulatorExchange = "accumulator-exchange"
	// Providers dispute claim decisions (reconsiderations, appeals) from the
	// provider portal; disputes are worked in claims.
	Disputes = "disputes"
)

func claimsDemos() []Demo {
	return []Demo{
		{
			ID: "adjudication", Title: "Claim Adjudication", Status: Live, Path: "/adjudication/",
			Summary: "Inside adjudication: each step a claim goes through, what it can pend or deny, and what it asks other domains.",
			Links:   []Link{{Component: Adjudication}},
		},
		{
			ID: "claims-lifecycle", Title: "Claims Lifecycle", Status: Live, Path: "/claims-lifecycle/",
			Summary:   "A claim from intake to payment: adjudication, accumulators, pends, adjustments and voids.",
			Scenarios: len(scenario.Library),
			Links: []Link{
				{Component: Adjudication},
				{Component: Settlement, Group: "Payments"},
			},
		},
		{
			ID: "claims-intake", Title: "Claims Intake", Status: Live, Path: "/claims-intake/",
			Summary: "837P files from the clearinghouse: the 999 for each transaction set, and each claim exactly as mapped from the file.",
			Links:   []Link{{Component: Clearinghouse}, {Component: ClaimsIntake}},
		},
		{
			ID: "remittance", Title: "Remittance", Status: Live, Path: "/remittance/",
			Summary: "From adjudication result to 835: the CAS breakdown, denials, batches, and reversals of corrected and voided claims.",
			Links:   []Link{{Component: Remittance}},
		},
		{
			ID: "service-definitions", Title: "Service Definitions", Status: Live, Path: "/service-definitions/",
			Summary: "Named groups of services by code: view and edit a group's codes and their dates.",
			Links:   []Link{{Component: ServiceDefinitions}},
		},
		{
			ID: "settlement", Title: "Settlement", Status: Planned,
			Summary: "What each claim will pay, to whom and from whose funds, before it is adjudicated; payment cycles net recoveries across claims.",
			Links:   []Link{{Component: Settlement}},
		},
		{
			ID: "overpayments", Title: "Overpayments", Status: Planned,
			Summary: "What a payee holds beyond what an adjusted or voided claim now owes them.",
			Links:   []Link{{Component: Overpayments}},
		},
		{
			ID: "external-claims", Title: "Externally Processed Claims", Status: Planned,
			Summary: "Claims a PBM or other vendor adjudicates and pays: their paid claims in, and shared accumulators exchanged both ways.",
			Links: []Link{
				{Component: ExternalProcessors}, {Component: ExternalClaims},
				{Component: ExternalClaimsIntake}, {Component: AccumulatorExchange},
			},
		},
	}
}
