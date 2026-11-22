// Package demos is the catalog of interactive demos and how they map onto
// the domain map. The demo server's landing page draws the map and uses
// this catalog to make components clickable.
//
// Each domain lists its own components and demos in its own file
// (employer.go, claims.go, network.go, finance.go, medical.go), so work on
// one domain doesn't touch another's file. Component ids match the nodes in
// the map (cmd/demo/web/index.html).
package demos

// Status of a demo.
const (
	Live    = "live"
	Planned = "planned"
)

// Journey is the admin portal: our staff's view of every domain.
const Journey = "journey"

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

var platformDemos = []Demo{
	{
		ID: "journey", Title: "Journey", Status: Live, Path: "/journey/",
		Summary: "The admin portal: examiners work pend queues, review flags and holds, and adjust claims.",
		Links:   []Link{{Component: Journey}},
	},
}

// Catalog lists every demo, live and planned, domain by domain.
func Catalog() []Demo {
	var all []Demo
	for _, d := range [][]Demo{platformDemos, claimsDemos(), employerDemos, networkDemos, financeDemos, medicalDemos} {
		all = append(all, d...)
	}
	return all
}
