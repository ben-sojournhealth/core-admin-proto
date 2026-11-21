package journey_test

import (
	"coreadmin/internal/repo/repotest"
	"regexp"
	"testing"

	"coreadmin/internal/journey"
)

// The workload must play cleanly and leave the queues the demo relies on.
func TestWorkload(t *testing.T) {
	s, err := journey.NewIn(repotest.New(t))
	if err != nil {
		t.Fatal(err)
	}
	st, err := journey.Snapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	pends := map[string]int{}
	flags, holds := 0, 0
	for _, c := range st.Claims {
		for _, p := range c.Pends {
			pends[p.Code]++
		}
		for _, f := range c.Flags {
			if f.Open {
				flags++
			}
		}
		if c.Hold != "" {
			holds++
		}
		t.Logf("%-10s %-9s %-10s pays %6d %v", c.ID, c.Status, c.Member, c.Paid, c.Pends)
	}
	want := map[string]int{"PRIOR_AUTH_REQUIRED": 2, "POSSIBLE_DUPLICATE": 1, "NO_BENEFIT_MATCH": 1, "NO_ELIGIBILITY": 1}
	for code, n := range want {
		if pends[code] != n {
			t.Errorf("%s pends: got %d, want %d", code, pends[code], n)
		}
	}
	if flags != 1 || holds != 1 {
		t.Errorf("open flags %d, holds %d; want 1 and 1", flags, holds)
	}
	// Every claim has a UUIDv7 id and a claim number; the adjustment of
	// 26063-A-00001 is version 2 of the same number.
	uuid7 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	byNumber := map[string]string{}
	for _, c := range st.Claims {
		if !uuid7.MatchString(c.ID) {
			t.Errorf("claim %s has id %q, not a UUIDv7", c.Number, c.ID)
		}
		byNumber[c.Number] = c.ID
	}
	for _, n := range []string{"26061-A-00001", "26062-A-00002", "26063-A-00001", "26063-A-00001-2", "26075-A-00001"} {
		if byNumber[n] == "" {
			t.Errorf("no claim numbered %s", n)
		}
	}

	d, err := journey.GetDetail(s, byNumber["26065-A-00001"])
	if err != nil {
		t.Fatal(err)
	}
	if d.Card != "ACM100101" || len(d.Timeline) == 0 {
		t.Errorf("detail: card %q, %d timeline events", d.Card, len(d.Timeline))
	}
}
