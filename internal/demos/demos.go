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
	Ingestion     = "ingestion"
	PlanDesign    = "plan-design"
	Eligibility   = "eligibility"
	ProviderData  = "provider-data"
	Adjudication  = "adjudication"
	Accumulators  = "accumulators"
	ClaimOps      = "claim-ops" // pends, adjustments, voids
	Payments      = "payments"
	StopLoss      = "stop-loss"
	Journey       = "journey" // the claims examiner portal
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
				{Component: Adjudication, Group: "Basics"},
				{Component: Accumulators, Group: "Accumulators"},
				{Component: ClaimOps, Group: "Pends"},
				{Component: ClaimOps, Group: "Adjustments"},
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
			Links:   []Link{{Component: Enrollment}, {Component: Ingestion}, {Component: Eligibility}},
		},
		{
			ID: "provider-data", Title: "Provider Data", Status: Planned,
			Summary: "Roster loads, network participation, contracts and how they change pricing.",
			Links:   []Link{{Component: Rosters}, {Component: Ingestion}, {Component: ProviderData}},
		},
		{
			ID: "plan-design", Title: "Plan Design", Status: Planned,
			Summary: "Configure a customer's benefits and test the design against claims.",
			Links:   []Link{{Component: PlanDesign}},
		},
		{
			ID: "claims-intake", Title: "Claims Intake", Status: Live, Path: "/claims-intake/",
			Summary: "837P files from the clearinghouse: the 999, corrected claims and voids, and claims that pend instead of bouncing.",
			Links:   []Link{{Component: Clearinghouse}, {Component: Ingestion}},
		},
		{
			ID: "payments-remittance", Title: "Payments & Remittance", Status: Planned,
			Summary: "Payment runs, 835s, recoveries netted against later payments.",
			Links:   []Link{{Component: Payments}},
		},
		{
			ID: "stop-loss", Title: "Stop-Loss", Status: Planned,
			Summary: "Specific and aggregate exposure, lasers, contract windows and filings.",
			Links:   []Link{{Component: StopLoss}},
		},
	}
}
