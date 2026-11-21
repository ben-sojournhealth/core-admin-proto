package eligibilitydemo_test

import (
	"context"
	"strings"
	"testing"

	"coreadmin/internal/eligibility"
	"coreadmin/internal/eligibilitydemo"
)

// The walkthrough's examples say what the slides claim they say.
func TestTour(t *testing.T) {
	tour, err := eligibilitydemo.BuildTour(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range tour.Checks {
		if !c.OK {
			t.Errorf("check %s: %s", c.Name, c.Detail)
		}
	}
	r := tour.Examples["retro"]
	answers := []string{}
	for _, q := range r.Questions {
		answers = append(answers, q.About+" "+q.Answer)
	}
	if got := strings.Join(answers, "; "); got != "2026-02-10 covered, employee (v1); 2026-02-10 not covered (v2); 2026-02-10 covered, employee (v3); 2026-01-20 covered, employee (v2)" {
		t.Errorf("retro answers: %s", got)
	}
	if len(r.Versions) != 3 || len(r.Assertions) != 3 {
		t.Errorf("retro: %d versions, %d assertions", len(r.Versions), len(r.Assertions))
	}
	ff := tour.Examples["fullfile"].FullFile
	var left []string
	for _, p := range ff.LeftOut {
		left = append(left, p.Name+" ("+p.EnrolledBy+"): "+spansOf(p.Before)+" → "+spansOf(p.After))
	}
	if len(ff.Enrolled) != 7 || len(ff.InFile) != 4 || ff.VersionsBeforeApproval != 0 ||
		strings.Join(left, "; ") != "Noah Rivera (Noah Rivera · addition): 2026-03-02.. → 2026-03-02..2026-03-31; "+
			"Arjun Patel (Arjun Patel · audit): 2026-01-01.. → 2026-01-01..2026-03-31; "+
			"Priya Patel (Priya Patel · audit): 2026-01-01.. → 2026-01-01..2026-03-31" {
		t.Errorf("full file: enrolled %v, in file %v, %d versions before approval, left out: %s", ff.Enrolled, ff.InFile, ff.VersionsBeforeApproval, strings.Join(left, "; "))
	}
	c := tour.Examples["cobra"]
	ignored := 0
	for _, a := range c.Assertions {
		if a.Ignored {
			ignored++
		}
	}
	if ignored != 1 || c.Questions[0].Answer[:14] != "covered, COBRA" {
		t.Errorf("cobra: %d ignored, %v", ignored, c.Questions)
	}
	if e := tour.Examples["exceptions"]; e.VersionsAdded != 0 || len(e.Steps) != 2 || e.Steps[0].Status != "EXCEPTION" {
		t.Errorf("exceptions: %+v", e)
	}
	g := tour.Examples["regen"]
	rb := g.Facts["rebuilt"].(*eligibilitydemo.Rebuilt)
	changed := g.Facts["changed"].([]map[string]any)
	if rb.Differences != 0 || rb.Members != 13 || len(changed) != 1 || changed[0]["name"] != "Priya Patel" {
		t.Errorf("regen: rebuilt %+v, changed %v", rb, changed)
	}
}

func spansOf(ss []eligibility.SpanView) string {
	var out []string
	for _, s := range ss {
		out = append(out, s.From+".."+s.To)
	}
	return strings.Join(out, ", ")
}
