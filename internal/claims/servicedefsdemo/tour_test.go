package servicedefsdemo_test

import (
	"context"
	"strings"
	"testing"

	"coreadmin/internal/claims/servicedefsdemo"
)

// The "How it works" tab's examples say what the page claims they say.
func TestTour(t *testing.T) {
	tour, err := servicedefsdemo.BuildTour(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if g := tour.Group; g.Name != "Office visit" || len(g.Systems) != 3 || len(g.Systems[0].Codes) != 2 || len(g.Systems[1].Codes) != 3 || len(g.Systems[2].Codes) != 0 {
		t.Errorf("group: %+v", g)
	}
	want := []string{"Office visit", "Emergency room", "Advanced imaging (CT/MRI)+Emergency room", "Inpatient facility", ""}
	if len(tour.Lines) != len(want) {
		t.Fatalf("%d lines, want %d", len(tour.Lines), len(want))
	}
	for i, l := range tour.Lines {
		if got := strings.Join(l.Groups, "+"); got != want[i] {
			t.Errorf("%s: in %q, want %q", l.What, got, want[i])
		}
	}
	// The same line is in no group the day before place of service 10
	// starts counting, and in Office visit from the day it does.
	if d := tour.Dates; len(d) != 2 || len(d[0].Groups) != 0 || d[0].DateOfService != "2021-12-31" || strings.Join(d[1].Groups, "+") != "Office visit" {
		t.Errorf("dates: %+v", tour.Dates)
	}
	// Five revisions, newest first; five rows, oldest first, each closed
	// on the day the next opens and the last still open.
	if r := tour.Revisions; len(r) != 5 || r[0].Revision != 5 || r[0].Saved != "2022-02-10" || r[4].Saved != "2019-03-04" || r[4].Codes != 2 {
		t.Errorf("revisions: %+v", tour.Revisions)
	}
	if len(tour.Rows) != 5 {
		t.Fatalf("%d rows, want 5", len(tour.Rows))
	}
	for i, row := range tour.Rows {
		next := ""
		if i+1 < len(tour.Rows) {
			next = tour.Rows[i+1].TxFrom
		}
		if row.Revision != int64(i+1) || row.TxTo != next {
			t.Errorf("row %d: %+v, want it closed on %q", i+1, row, next)
		}
	}
	if !strings.Contains(tour.Data, `"office-visit"`) || !strings.Contains(tour.Data, `"audit"`) || !strings.Contains(tour.Data, "CODE_SYSTEM_PLACE_OF_SERVICE") {
		t.Errorf("data:\n%s", tour.Data)
	}
	if len(tour.Endpoints) != 6 {
		t.Errorf("%d endpoints, want 6", len(tour.Endpoints))
	}
	for _, e := range tour.Endpoints {
		if e.Doc == "." || !strings.HasPrefix(e.Doc, e.Name) {
			t.Errorf("%s: doc %q", e.Name, e.Doc)
		}
	}
}
