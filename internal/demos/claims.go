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
	// viewable before the claim is finalized; payment details fill in once
	// it pays.
	Remittance = "remittance"
	// What each claim pays, to whom and from whose money: a pure function of
	// the claim, previewable before it finalizes; the payment cycle nets
	// recorded lines across claims.
	Settlement = "settlement"
	// Providers dispute claim decisions (reconsiderations, appeals) from the
	// provider portal; disputes are worked in claims.
	Disputes = "disputes"
)

func claimsDemos() []Demo {
	return []Demo{
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
			ID: "settlement", Title: "Settlement", Status: Planned,
			Summary: "What each claim will pay, to whom and from whose funds, before it finalizes; payment cycles net recoveries across claims.",
			Links:   []Link{{Component: Settlement}},
		},
	}
}
