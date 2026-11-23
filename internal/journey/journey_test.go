package journey_test

import (
	"coreadmin/internal/common/repo/repotest"
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
	for _, c := range st.Claims {
		for _, p := range c.Pends {
			pends[p.Code]++
		}
		t.Logf("%-10s %-9s %-10s pays %6d %v", c.ID, c.Status, c.Member, c.Paid, c.Pends)
	}
	want := map[string]int{"PRIOR_AUTH_REQUIRED": 2, "POSSIBLE_DUPLICATE": 1, "NO_BENEFIT_MATCH": 1, "NO_ELIGIBILITY": 1, "REOPENED": 1}
	for code, n := range want {
		if pends[code] != n {
			t.Errorf("%s pends: got %d, want %d", code, pends[code], n)
		}
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

// The tabs beside Claims read every domain's stores as the workload left
// them.
func TestAdmin(t *testing.T) {
	s, err := journey.NewIn(repotest.New(t))
	if err != nil {
		t.Fatal(err)
	}
	a, err := journey.GetAdmin(s)
	if err != nil {
		t.Fatal(err)
	}
	// Coverage on the sandbox's date: Avery isn't enrolled, and Lucia's
	// coverage ended in February.
	states := map[string]string{}
	for _, m := range a.Members {
		states[m.Name] = m.State
	}
	for name, want := range map[string]string{"Jordan Rivera": "ACTIVE", "Avery Rivera": "NONE", "Lucia Morales": "TERMINATED", "Ada Okafor": "ACTIVE"} {
		if states[name] != want {
			t.Errorf("%s: coverage %q, want %q", name, states[name], want)
		}
	}
	if len(a.Members) != 9 {
		t.Errorf("%d members, want 9", len(a.Members))
	}

	var acme *journey.EmployerView
	for i := range a.Employers {
		if a.Employers[i].ID == "cust_acme" {
			acme = &a.Employers[i]
		}
	}
	if acme == nil {
		t.Fatal("no Acme")
	}
	subscribers := 0
	for _, tc := range acme.Tiers {
		subscribers += tc.Subscribers
	}
	// Rivera, Patel and Okafor; Morales is terminated.
	if subscribers != 3 || len(acme.Senders) != 2 || len(acme.Plans) != 1 {
		t.Errorf("Acme: %d subscribers, %d senders, %d plans; want 3, 2, 1", subscribers, len(acme.Senders), len(acme.Plans))
	}
	if p := acme.Plans[0]; len(p.Tiers) != 2 || len(p.Tiers[0].Networks) != 1 || p.Tiers[0].Networks[0] != "Mainline" || len(p.Tiers[1].Networks) != 0 || p.Tiers[0].DeductibleIndividual != 150000 || p.Tiers[0].OOPFamily != 1000000 || len(p.Benefits) == 0 {
		t.Errorf("Acme plan: %+v", p.Tiers)
	}
	for _, b := range acme.Plans[0].Benefits {
		if len(b.Shares) != 2 || b.Shares[0] == nil {
			t.Errorf("benefit %s: shares %v", b.Category, b.Shares)
		}
	}

	in := map[string]bool{}
	for _, p := range a.Providers {
		in[p.NPI] = p.InNetwork
	}
	if len(in) != 2 || !in["1234567893"] || in["1987654321"] {
		t.Errorf("providers in network: %v", in)
	}
	// The underlying networks: direct contracts (no providers yet) and the
	// rented PPO.
	if len(a.Networks) != 2 || a.Networks[0].ID != "net_direct" || a.Networks[0].Providers != 0 || a.Networks[1].Providers != 1 || len(a.Contracts) != 1 || len(a.Contracts[0].Terms) != 2 {
		t.Errorf("networks %+v, contracts %+v", a.Networks, a.Contracts)
	}

	if len(a.StopLoss) != 1 {
		t.Fatalf("%d stop-loss policies, want 1", len(a.StopLoss))
	}
	sl := a.StopLoss[0]
	if sl.Deductible != 10000000 || sl.RunOutMonths != 3 || len(sl.Lasers) != 1 || sl.Lasers[0].Member != "Sam Rivera" || len(sl.Factors) != 4 {
		t.Errorf("stop-loss: %+v", sl)
	}
	// Nothing posts paid claims to stop-loss yet, and the workload loads
	// no files.
	if len(sl.Specific) != 0 || len(a.Files) != 0 {
		t.Errorf("%d exposures, %d files; want none", len(sl.Specific), len(a.Files))
	}
}
