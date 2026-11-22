// Package demos is the catalog of interactive demos and how they map onto
// the system architecture. The demo server's landing page draws the
// architecture diagram and uses this catalog to make components clickable.
package demos

import "coreadmin/internal/scenario"

// Status of a demo.
const (
	Live    = "live"
	Planned = "planned"
)

// Component ids, matching the nodes in the architecture diagram
// (cmd/demo/web/index.html).
const (
	Enrollment     = "enrollment"      // 834 files from employers
	Clearinghouse  = "clearinghouse"   // 837 claims
	NetworkVendors = "network-vendors" // network and provider files from vendors
	Rosters        = "rosters"         // providers' rosters and contracts
	// Each source is taken in its own way: its own format, checks and
	// acknowledgment.
	EnrollmentIntake = "enrollment-intake" // 834
	ClaimsIntake     = "claims-intake"     // 837, 999
	VendorIntake     = "vendor-intake"     // network vendor files
	RosterIntake     = "roster-intake"     // rosters; out of scope for '28
	PlanDesign       = "plan-design"
	Eligibility      = "eligibility"
	// Funding arrangements, funding requests and claims registers, and
	// admin-fee billing.
	BillingFunding = "billing-funding"
	ProviderData   = "provider-data" // provider data management, directory
	Contracting    = "contracting"   // provider contracts; pricing them is adjudication's
	Credentialing  = "credentialing" // verifying providers before they join a network
	// Named groups of procedure codes (burn treatment, blood transfusion)
	// that adjudication's rules refer to.
	ServiceDefinitions = "service-definitions"
	Adjudication       = "adjudication" // pricing, cost share, accumulators, pends, adjustments, voids
	// Medical management: usually contracted out by a TPA.
	MedicalPolicy = "medical-policy" // criteria, auth-required lists
	Authorization = "authorization"  // prior auth, UM review; matching auths to claims is adjudication's
	// Providers dispute claim decisions (reconsiderations, appeals) from the
	// provider portal; disputes are worked in claims.
	Disputes = "disputes"
	// Disputes and (out of scope for '28) prior auth requests from providers.
	ProviderPortal = "provider-portal"
	// What each claim pays, to whom and from whose money: a pure function of
	// the claim, previewable before it finalizes; the payment cycle nets
	// recorded lines across claims.
	Settlement = "settlement"
	// Finance: moving the money (funding, EFT, checks, reconciliation) and
	// stop-loss.
	Disbursement = "disbursement"
	// 835s, provider EOPs and member EOBs: built from the adjudication,
	// viewable before the claim is finalized; payment details fill in once
	// it pays.
	Remittance = "remittance"
	StopLoss   = "stop-loss"
	Journey    = "journey" // the admin portal, over everything
)

// Link points a diagram component at a demo, optionally at one scenario
// group within it.
type Link struct {
	Component string `json:"component"`
	Group     string `json:"group,omitempty"`
}

type Demo struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
	// Path the demo is served at; empty for planned demos.
	Path      string `json:"path,omitempty"`
	Links     []Link `json:"links"`
	Scenarios int    `json:"scenarios"`
}

// Catalog lists every demo, live and planned.
func Catalog() []Demo {
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
			ID: "journey", Title: "Journey", Status: Live, Path: "/journey/",
			Summary: "The admin portal: examiners work pend queues, review flags and holds, and adjust claims.",
			Links:   []Link{{Component: Journey}},
		},
		{
			ID: "enrollment-intake", Title: "Enrollment Intake", Status: Live, Path: "/enrollment-intake/",
			Summary: "834 files from employers: the 999 for each transaction set, and each member loop exactly as mapped from the file.",
			Links:   []Link{{Component: Enrollment}, {Component: EnrollmentIntake}},
		},
		{
			ID: "eligibility", Title: "Eligibility", Status: Live, Path: "/eligibility/",
			Summary: "Members derived from every 834 input: each version as it was believed, flip-flops, full files with an approval gate, regeneration.",
			Links:   []Link{{Component: Eligibility}},
		},
		{
			ID: "provider-data", Title: "Provider Data", Status: Planned,
			Summary: "Roster loads, network participation, contracting and the provider directory.",
			Links:   []Link{{Component: NetworkVendors}, {Component: Rosters}, {Component: VendorIntake}, {Component: RosterIntake}, {Component: ProviderData}, {Component: Contracting}, {Component: Credentialing}},
		},
		{
			ID: "plan-design", Title: "Plan Design", Status: Planned,
			Summary: "Configure a customer's benefits and test the design against claims.",
			Links:   []Link{{Component: PlanDesign}},
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
		{
			ID: "disbursement", Title: "Disbursement", Status: Planned,
			Summary: "Employer funding, EFT and checks, and bank reconciliation.",
			Links:   []Link{{Component: Disbursement}},
		},
		{
			ID: "stop-loss", Title: "Stop-Loss", Status: Planned,
			Summary: "Specific and aggregate exposure, lasers, contract windows and filings.",
			Links:   []Link{{Component: StopLoss}},
		},
	}
}
