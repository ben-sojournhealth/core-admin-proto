package employer_test

import (
	"context"
	"testing"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	employerv1 "coreadmin/gen/coreadmin/employer/v1"
	planv1 "coreadmin/gen/coreadmin/plan/v1"
	"coreadmin/internal/common/repo/repotest"
	"coreadmin/internal/employer"
)

func d(y, m, day int32) *commonv1.Date { return &commonv1.Date{Year: y, Month: m, Day: day} }

func span(id string, from, to *commonv1.Date, st eligibilityv1.CoverageStatus) *eligibilityv1.CoverageSpan {
	return &eligibilityv1.CoverageSpan{Id: id, PlanId: "plan_x", Line: eligibilityv1.CoverageLine_COVERAGE_LINE_MEDICAL,
		Effective: &commonv1.DateRange{Start: from, End: to}, Status: st}
}

// Coverage is answered for the date asked, from the latest data: a
// retro-termination recorded later changes the answer for an earlier
// date of service.
func TestMedicalCoverageOnADate(t *testing.T) {
	r := repotest.New(t)
	ctx := context.Background()
	var api employer.API = employer.New(r)
	coverage := func(on *commonv1.Date) *employerv1.GetMedicalCoverageResponse {
		t.Helper()
		c, err := api.GetMedicalCoverage(ctx, &employerv1.GetMedicalCoverageRequest{MemberId: "m1", On: on})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	active := eligibilityv1.CoverageStatus_COVERAGE_STATUS_ACTIVE
	m, err := r.Members.Create(ctx, &eligibilityv1.Member{Id: "m1", CustomerId: "c1", CardId: "ACM1", SubscriberId: "m1",
		Coverage: []*eligibilityv1.CoverageSpan{span("s1", d(2026, 1, 1), nil, active)}})
	if err != nil {
		t.Fatal(err)
	}

	jan10 := d(2026, 1, 10)
	if c := coverage(jan10); c.GetSpan().GetId() != "s1" || !c.GetStartedBefore() {
		t.Fatalf("covered on Jan 10: %v", c)
	}
	if c := coverage(jan10); c.GetNetworkId() != "" {
		t.Errorf("network before the plan is known: %v", c)
	}

	// The member's network is the one their plan uses on the date asked.
	if _, err := r.Plans.Create(ctx, &planv1.Plan{Id: "plan_x", Versions: []*planv1.PlanVersion{
		{Version: 1, Effective: &commonv1.DateRange{Start: d(2026, 1, 1), End: d(2026, 6, 30)}, NetworkId: "net_a"},
		{Version: 2, Effective: &commonv1.DateRange{Start: d(2026, 7, 1), End: d(2026, 12, 31)}, NetworkId: "net_b"},
	}}); err != nil {
		t.Fatal(err)
	}
	if c := coverage(jan10); c.GetNetworkId() != "net_a" {
		t.Errorf("network on Jan 10: %v", c)
	}
	if c := coverage(d(2026, 8, 1)); c.GetNetworkId() != "net_b" {
		t.Errorf("network after the mid-year amendment: %v", c)
	}
	if c := coverage(d(2027, 2, 1)); c.GetSpan() == nil || c.GetNetworkId() != "" {
		t.Errorf("covered, but no plan version in force: %v", c)
	}
	if c := coverage(d(2025, 12, 31)); c.GetNetworkId() != "" {
		t.Errorf("network without coverage: %v", c)
	}
	if c := coverage(d(2025, 12, 31)); c.GetSpan() != nil || c.GetStartedBefore() {
		t.Errorf("before coverage began: %v", c)
	}

	// Later, the employer retro-terminates coverage to Jan 5. The same
	// question about Jan 10 now gets the new answer.
	m.Coverage = []*eligibilityv1.CoverageSpan{
		span("s1", d(2026, 1, 1), d(2026, 1, 5), active),
		span("s1t", d(2026, 1, 6), nil, eligibilityv1.CoverageStatus_COVERAGE_STATUS_TERMINATED),
	}
	if _, err := r.Members.Update(ctx, m); err != nil {
		t.Fatal(err)
	}
	if c := coverage(jan10); c.GetSpan().GetStatus() != eligibilityv1.CoverageStatus_COVERAGE_STATUS_TERMINATED || !c.GetStartedBefore() {
		t.Errorf("after the retro-term, Jan 10: %v", c)
	}
	if c := coverage(d(2026, 1, 3)); c.GetSpan().GetId() != "s1" {
		t.Errorf("after the retro-term, Jan 3 is still covered: %v", c)
	}
}

func TestPlanVersionInForce(t *testing.T) {
	r := repotest.New(t)
	ctx := context.Background()
	var api employer.API = employer.New(r)
	version := func(plan string, on *commonv1.Date) (*employerv1.GetPlanVersionResponse, error) {
		return api.GetPlanVersion(ctx, &employerv1.GetPlanVersionRequest{PlanId: plan, On: on})
	}
	r.Plans.Create(ctx, &planv1.Plan{Id: "plan_x", Versions: []*planv1.PlanVersion{
		{Version: 1, Effective: &commonv1.DateRange{Start: d(2026, 1, 1), End: d(2026, 6, 30)}},
		{Version: 2, Effective: &commonv1.DateRange{Start: d(2026, 7, 1), End: d(2026, 12, 31)}},
	}})
	if v, err := version("plan_x", d(2026, 8, 1)); err != nil || v.GetVersion().GetVersion() != 2 {
		t.Errorf("August: %v %v", v, err)
	}
	if v, err := version("plan_x", d(2027, 1, 1)); err != nil || v.GetPlan().GetId() != "plan_x" || v.GetVersion() != nil {
		t.Errorf("2027, no version in force: %v %v", v, err)
	}
	if _, err := version("plan_y", d(2026, 8, 1)); !employer.NotFound(err) {
		t.Errorf("unknown plan: %v", err)
	}
}

// A claim's member is matched from what the claim submitted: the
// subscriber's member number, and for a patient sent apart, first name and
// birth date within that household.
func TestMatchClaimMember(t *testing.T) {
	r := repotest.New(t)
	ctx := context.Background()
	var api employer.API = employer.New(r)
	for _, m := range []*eligibilityv1.Member{
		{Id: "m1", CustomerId: "c1", SubscriberId: "m1", CardId: "ACM1", Name: &commonv1.PersonName{First: "Jordan", Last: "Rivera"}, BirthDate: d(1985, 4, 12)},
		{Id: "m2", CustomerId: "c1", SubscriberId: "m1", CardId: "ACM2", Name: &commonv1.PersonName{First: "Riley", Last: "Rivera"}, BirthDate: d(2015, 6, 21)},
		// The same member number at two customers.
		{Id: "m3", CustomerId: "c1", SubscriberId: "m3", CardId: "DUP1"},
		{Id: "m4", CustomerId: "c2", SubscriberId: "m4", CardId: "DUP1"},
	} {
		if _, err := r.Members.Create(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	const (
		matched   = employerv1.MemberMatchOutcome_MEMBER_MATCH_OUTCOME_MATCHED
		notFound  = employerv1.MemberMatchOutcome_MEMBER_MATCH_OUTCOME_NOT_FOUND
		ambiguous = employerv1.MemberMatchOutcome_MEMBER_MATCH_OUTCOME_AMBIGUOUS
	)
	sub := func(number string) *employerv1.ClaimPerson { return &employerv1.ClaimPerson{MemberNumber: number} }
	patient := func(first string, born *commonv1.Date) *employerv1.ClaimPerson {
		return &employerv1.ClaimPerson{Name: &commonv1.PersonName{First: first, Last: "RIVERA"}, BirthDate: born}
	}
	for _, tc := range []struct {
		name    string
		sub, pt *employerv1.ClaimPerson
		outcome employerv1.MemberMatchOutcome
		member  string
	}{
		{"the subscriber is the patient", sub("ACM1"), nil, matched, "m1"},
		{"a dependent's own member number", sub("ACM2"), nil, matched, "m2"},
		{"a dependent in the patient loop", sub("ACM1"), patient("RILEY", d(2015, 6, 21)), matched, "m2"},
		{"a patient with another birth date", sub("ACM1"), patient("RILEY", d(2015, 6, 22)), notFound, ""},
		{"a patient not in the household", sub("ACM1"), patient("NOAH", d(2015, 6, 21)), notFound, ""},
		{"an unknown member number", sub("ACM9"), nil, notFound, ""},
		{"no member number", sub(""), nil, notFound, ""},
		{"a member number at two customers", sub("DUP1"), nil, ambiguous, ""},
	} {
		res, err := api.MatchClaimMember(ctx, &employerv1.MatchClaimMemberRequest{Subscriber: tc.sub, Patient: tc.pt})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.GetOutcome() != tc.outcome || res.GetMember().GetId() != tc.member {
			t.Errorf("%s: %s %q, want %s %q", tc.name, res.GetOutcome(), res.GetMember().GetId(), tc.outcome, tc.member)
		}
	}
}
