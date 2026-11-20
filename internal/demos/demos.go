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
	Enrollment    = "enrollment"    // 834 files from employers
	Clearinghouse = "clearinghouse" // 837 claims
	Rosters       = "rosters"       // provider rosters, contracts, fee schedules
	// Each source is taken in its own way: its own format, checks and
	// acknowledgment.
	EnrollmentIntake = "enrollment-intake" // 834
	ClaimsIntake     = "claims-intake"     // 837, 999
	ProviderIntake   = "provider-intake"   // rosters
	PlanDesign       = "plan-design"
	Eligibility      = "eligibility"
	ProviderData     = "provider-data"
	Adjudication     = "adjudication" // pricing, cost share, accumulators, pends, adjustments, voids
	Payments         = "payments"
	// 835s, provider EOPs and member EOBs: built from the adjudication,
	// viewable before the claim is finalized; payment details fill in once
	// it pays.
	Remittance = "remittance"
	StopLoss   = "stop-loss"
	Journey    = "journey" // the claims examiner portal
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
				{Component: Payments, Group: "Payments"},
			},
		},
		{
			ID: "journey", Title: "Journey", Status: Live, Path: "/journey/",
			Summary: "The claims portal: examiners work pend queues, review flags and holds, and adjust claims.",
			Links:   []Link{{Component: Journey}},
		},
		{
			ID: "eligibility-ingestion", Title: "Eligibility Ingestion", Status: Planned,
			Summary: "834 full and change files: adds, terms, retro changes and the approval gate.",
			Links:   []Link{{Component: Enrollment}, {Component: EnrollmentIntake}, {Component: Eligibility}},
		},
		{
			ID: "provider-data", Title: "Provider Data", Status: Planned,
			Summary: "Roster loads, network participation, contracts and how they change pricing.",
			Links:   []Link{{Component: Rosters}, {Component: ProviderIntake}, {Component: ProviderData}},
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
			ID: "payments", Title: "Payments", Status: Planned,
			Summary: "Payment runs, EFT and checks, and recoveries netted against later payments.",
			Links:   []Link{{Component: Payments}},
		},
		{
			ID: "stop-loss", Title: "Stop-Loss", Status: Planned,
			Summary: "Specific and aggregate exposure, lasers, contract windows and filings.",
			Links:   []Link{{Component: StopLoss}},
		},
	}
}
