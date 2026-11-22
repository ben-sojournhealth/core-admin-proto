// Package journey backs the Journey demo: the claims examiner's
// workbench. It runs its own sandbox, separate from Claims Lifecycle,
// pre-loaded with two weeks of claims for several families so the work
// queues start with real work in them.
package journey

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"time"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	"coreadmin/internal/claims"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
	"coreadmin/internal/sim"
)

// Today is the date the workbench opens on.
var Today = time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)

const ortiz = "1987654321"

type family struct {
	subscriber string
	members    []member
	// Coverage end (YYYY-MM-DD), empty for active coverage.
	end string
}

type member struct {
	id, first, last, rel string
	born                 *commonv1.Date
}

// Families added on top of the seeded Rivera family.
var families = []family{
	{subscriber: "mem_2001", members: []member{
		{"mem_2001", "Priya", "Patel", "SELF", &commonv1.Date{Year: 1979, Month: 11, Day: 2}},
		{"mem_2002", "Arjun", "Patel", "SPOUSE", &commonv1.Date{Year: 1977, Month: 5, Day: 19}},
	}},
	{subscriber: "mem_3001", members: []member{
		{"mem_3001", "Chidi", "Okafor", "SELF", &commonv1.Date{Year: 1990, Month: 2, Day: 8}},
		{"mem_3002", "Ada", "Okafor", "CHILD", &commonv1.Date{Year: 2019, Month: 8, Day: 30}},
	}},
	{subscriber: "mem_4001", end: "2026-02-28", members: []member{
		{"mem_4001", "Lucia", "Morales", "SELF", &commonv1.Date{Year: 1968, Month: 7, Day: 14}},
	}},
}

// NewIn plays the workload into repos, which must be empty.
func NewIn(repos *repo.Repos) (*sim.Sim, error) {
	s, err := sim.NewIn(repos)
	if err != nil {
		return nil, err
	}
	// Every claim gets a UUIDv7 id and is named by its claim number.
	s.UseShortIDs(false)
	if err := addFamilies(s); err != nil {
		return nil, err
	}
	s.SetClock(time.Date(2026, 3, 2, 8, 30, 0, 0, time.UTC))
	for _, day := range workload {
		s.SetClock(time.Date(2026, 3, day.day, 8, 30, 0, 0, time.UTC))
		for _, a := range day.actions {
			s.Advance(37 * time.Minute)
			if res := s.Apply(a); !res.OK {
				return nil, fmt.Errorf("journey workload, March %d, %s: %s", day.day, res.Title, res.Error)
			}
		}
	}
	s.SetClock(Today)
	return s, nil
}

func addFamilies(s *sim.Sim) error {
	ctx := store.WithActor(context.Background(), "enrollment")
	for _, f := range families {
		for i, m := range f.members {
			eff := &commonv1.DateRange{Start: &commonv1.Date{Year: 2026, Month: 1, Day: 1}}
			if f.end != "" {
				var y, mo, d int32
				fmt.Sscanf(f.end, "%d-%d-%d", &y, &mo, &d)
				eff.End = &commonv1.Date{Year: y, Month: mo, Day: d}
			}
			tier := eligibilityv1.CoverageTier_COVERAGE_TIER_EMPLOYEE
			if len(f.members) > 1 {
				tier = eligibilityv1.CoverageTier_COVERAGE_TIER_FAMILY
			}
			_, err := s.Repos.Members.Create(ctx, &eligibilityv1.Member{
				Id:           m.id,
				CustomerId:   sim.CustomerID,
				SubscriberId: f.subscriber,
				Relationship: eligibilityv1.Relationship(eligibilityv1.Relationship_value["RELATIONSHIP_"+m.rel]),
				Name:         &commonv1.PersonName{First: m.first, Last: m.last},
				BirthDate:    m.born,
				CardId:       fmt.Sprintf("ACM%s%02d", f.subscriber[4:], i+1),
				Coverage: []*eligibilityv1.CoverageSpan{{
					Id: "cov_" + m.id[4:] + "_med_2026", PlanId: sim.PlanID, GroupId: "grp_acme_main",
					Line: eligibilityv1.CoverageLine_COVERAGE_LINE_MEDICAL, Tier: tier,
					Effective: eff, Status: eligibilityv1.CoverageStatus_COVERAGE_STATUS_ACTIVE,
				}},
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func claim(member, procedure string, dollars int64, dos string) sim.Action {
	return sim.Action{Type: "process", Claim: &sim.ClaimSpec{Member: member, Procedure: procedure, Charge: dollars * 100, DOS: dos}}
}

func with(a sim.Action, f func(*sim.ClaimSpec)) sim.Action {
	f(a.Claim)
	return a
}

// workload is two weeks of claims. Later actions refer to earlier claims
// by claim number (two-digit year, day of year received, sequence); the
// comments give each claim's number.
var workload = []struct {
	day     int
	actions []sim.Action
}{
	{2, []sim.Action{
		claim("mem_1001", "99213", 180, "2026-02-26"), // 26061-A-00001 approved
	}},
	{3, []sim.Action{
		claim("mem_3001", "99213", 180, "2026-03-02"), // 26062-A-00001 approved
		claim("mem_4001", "99214", 250, "2026-03-02"), // 26062-A-00002 denied: coverage ended 2/28
	}},
	{4, []sim.Action{
		claim("mem_1001", "29881", 2500, "2026-03-03"), // 26063-A-00001 surgery, adjusted on the 12th
	}},
	{5, []sim.Action{
		claim("mem_2001", "99214", 250, "2026-03-04"), // 26064-A-00001 approved
		{Type: "select"},
		{Type: "release"},
	}},
	{6, []sim.Action{
		with(claim("mem_1001", "70551", 900, "2026-03-05"), func(c *sim.ClaimSpec) { c.Auth = "PA-1001" }), // 26065-A-00001 flagged on the 12th
	}},
	{9, []sim.Action{
		claim("mem_1002", "70551", 900, "2026-03-06"), // 26068-A-00001 pend: prior auth
	}},
	{10, []sim.Action{
		claim("mem_3001", "99213", 180, "2026-03-02"),                                                 // 26069-A-00001 pend: duplicate of 26062-A-00001
		with(claim("mem_3002", "99284", 1800, "2026-03-08"), func(c *sim.ClaimSpec) { c.POS = "23" }), // 26069-A-00002 ER, approved
	}},
	{11, []sim.Action{
		claim("mem_2002", "97110", 120, "2026-03-09"), // 26070-A-00001 pend: no benefit match
	}},
	{12, []sim.Action{
		claim("mem_1004", "99213", 180, "2026-03-10"), // 26071-A-00001 pend: newborn not enrolled
		{Type: "adjust", ClaimID: "26063-A-00001", Reason: "Provider corrected the procedure code", Claim: &sim.ClaimSpec{Procedure: "99213", Charge: 18000}},
	}},
	{13, []sim.Action{
		with(claim("mem_3001", "29881", 2500, "2026-03-11"), func(c *sim.ClaimSpec) { c.Provider = ortiz }), // 26072-A-00001 out-of-network
		claim("mem_1003", "99214", 250, "2026-03-12"),                                                       // 26072-A-00002 pulled from payment, on hold
		{Type: "select", ClaimID: "26072-A-00002"},
		{Type: "pull", ClaimID: "26072-A-00002", Reason: "Billing provider's TIN is under review"},
	}},
	{16, []sim.Action{
		claim("mem_2001", "70551", 900, "2026-03-13"), // 26075-A-00001 pend: prior auth
	}},
}

// State is what the workbench list needs.
type State struct {
	Today  string           `json:"today"`
	Claims []*sim.ClaimView `json:"claims"`
}

func Snapshot(s *sim.Sim) (*State, error) {
	all, _, err := s.Repos.Claims.List(context.Background(), store.ListOptions[*claimsv1.Claim]{PageSize: 10_000})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].GetReceivedTime().AsTime().Before(all[j].GetReceivedTime().AsTime())
	})
	st := &State{Today: s.Now().Format(time.RFC3339), Claims: []*sim.ClaimView{}}
	for _, c := range all {
		st.Claims = append(st.Claims, s.ClaimView(c))
	}
	return st, nil
}

// Detail is one claim with everything an examiner needs beside it.
type Detail struct {
	Claim        *sim.ClaimView       `json:"claim"`
	Card         string               `json:"card"`
	Accumulators sim.AccumulatorsView `json:"accumulators"`
	// Events that mention this claim, newest first.
	Timeline []*sim.LogEntry `json:"timeline"`
	// Claims linked to this one: its original, replacements and voids.
	Related []Related `json:"related"`
}

type Related struct {
	ID     string `json:"id"`
	Number string `json:"number"`
}

func GetDetail(s *sim.Sim, id string) (*Detail, error) {
	ctx := context.Background()
	c, err := s.Repos.Claims.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	d := &Detail{Claim: s.ClaimView(c), Timeline: []*sim.LogEntry{}, Related: []Related{}}
	if m, err := s.Repos.Members.Get(ctx, c.GetMemberId()); err == nil {
		d.Card = m.GetCardId()
		if d.Accumulators, err = s.FamilyAccumulators(m.GetId()); err != nil {
			return nil, err
		}
	}
	// The log names claims by number; an original's history includes its
	// later versions' events, which share the number.
	mention := regexp.MustCompile(regexp.QuoteMeta(claims.DisplayNumber(c)) + `\b`)
	log := s.Log()
	for i := len(log) - 1; i >= 0; i-- {
		e := log[i]
		hit := mention.MatchString(e.Title)
		for _, l := range e.Lines {
			hit = hit || mention.MatchString(l)
		}
		if hit {
			d.Timeline = append(d.Timeline, e)
		}
	}
	related, err := lineage(ctx, s, c)
	if err != nil {
		return nil, err
	}
	for _, r := range related {
		if r.GetId() != id {
			d.Related = append(d.Related, Related{ID: r.GetId(), Number: claims.DisplayNumber(r)})
		}
	}
	return d, nil
}

// lineage is every claim connected to c by original links, oldest first.
// Usually they share a claim number, but a correction linked after it was
// numbered keeps its own, so the links, not the number, define the family.
func lineage(ctx context.Context, s *sim.Sim, c *claimsv1.Claim) ([]*claimsv1.Claim, error) {
	// Up the original links to the root, then down through each claim's
	// replacements and voids (claims_by_original).
	root := c
	for seen := map[string]bool{}; root.GetOriginalClaimId() != "" && !seen[root.GetId()]; {
		seen[root.GetId()] = true
		p, err := s.Repos.Claims.Get(ctx, root.GetOriginalClaimId())
		if err != nil {
			break
		}
		root = p
	}
	var out []*claimsv1.Claim
	seen := map[string]bool{}
	for queue := []*claimsv1.Claim{root}; len(queue) > 0; queue = queue[1:] {
		x := queue[0]
		if seen[x.GetId()] {
			continue
		}
		seen[x.GetId()] = true
		out = append(out, x)
		children, _, err := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 1000, Where: []store.Cond{store.Eq(x.GetId(), "originalClaimId")}})
		if err != nil {
			return nil, err
		}
		queue = append(queue, children...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].GetReceivedTime().AsTime().Before(out[j].GetReceivedTime().AsTime())
	})
	return out, nil
}
