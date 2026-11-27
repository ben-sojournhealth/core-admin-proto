package claims_test

import (
	"context"
	"coreadmin/internal/common/repo/repotest"
	"strings"
	"testing"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	"coreadmin/internal/claims"
	"coreadmin/internal/common/store"
	"coreadmin/internal/journey"
	"coreadmin/internal/sim"
)

// These tests cover what happens to a claim after intake hands it over:
// matching the member and the original, numbering, and rematching on
// later runs. The claims are built as intake would submit them; the
// sandbox is Journey's, with its two weeks of claims.

func sandbox(t *testing.T) *sim.Sim {
	t.Helper()
	s, err := journey.NewIn(repotest.New(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// submitted is a one-line claim as an 837 would carry it: nothing matched.
func submitted(pcn, card, procedure string, dollars int64, day int32) *claimsv1.Claim {
	dos := &commonv1.DateRange{Start: &commonv1.Date{Year: 2026, Month: 3, Day: day}, End: &commonv1.Date{Year: 2026, Month: 3, Day: day}}
	return &claimsv1.Claim{
		Type: claimsv1.ClaimType_CLAIM_TYPE_PROFESSIONAL, Frequency: claimsv1.ClaimFrequency_CLAIM_FREQUENCY_ORIGINAL,
		Source: claimsv1.ClaimSource_CLAIM_SOURCE_EDI_837, PatientControlNumber: pcn, MemberCardId: card,
		BillingProvider: &claimsv1.ProviderRef{Npi: "1234567893", Tin: "311234567", Name: "MEI CHEN"},
		ServiceDates:    dos, TotalCharge: &commonv1.Money{Cents: dollars * 100},
		Lines: []*claimsv1.ClaimLine{{LineNumber: 1, ProcedureCode: procedure, PlaceOfServiceCode: "11", UnitsMilli: 1000,
			ServiceDates: dos, Charge: &commonv1.Money{Cents: dollars * 100}, DiagnosisPointers: []int32{1}}},
		DiagnosisCodes: []string{"J069"},
	}
}

func correcting(c *claimsv1.Claim, f claimsv1.ClaimFrequency, quoted string) *claimsv1.Claim {
	c.Frequency, c.PayerClaimControlNumber = f, quoted
	return c
}

func receive(t *testing.T, s *sim.Sim, c *claimsv1.Claim) *claimsv1.Claim {
	t.Helper()
	out, err := s.Claims.Receive(store.WithActor(context.Background(), "clearinghouse"), c)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func find(t *testing.T, s *sim.Sim, number string) *claimsv1.Claim {
	t.Helper()
	c, err := s.Repos.Claims.Get(context.Background(), s.Resolve(number))
	if err != nil {
		t.Fatalf("%s: %v", number, err)
	}
	return c
}

// describe is "number PENDED pend-codes", or once adjudicated "number
// OUTCOME".
func describe(c *claimsv1.Claim) string {
	if c.GetStatus() == claimsv1.ClaimStatus_CLAIM_STATUS_ADJUDICATED {
		return claims.DisplayNumber(c) + " " + strings.TrimPrefix(c.GetAdjudication().GetOutcome().String(), "CLAIM_OUTCOME_")
	}
	d := claims.DisplayNumber(c) + " " + strings.TrimPrefix(c.GetStatus().String(), "CLAIM_STATUS_")
	for _, p := range c.GetAdjudication().GetPends() {
		d += " " + p.GetCode()
	}
	return d
}

const (
	replacement = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT
	void        = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID
)

func TestReceiveMatchesAndNumbers(t *testing.T) {
	s := sandbox(t)
	riley := submitted("RIV-88240", "ACM100101", "99214", 250, 13)
	riley.SubmittedPatient = &claimsv1.SubmittedPerson{
		Name: &commonv1.PersonName{First: "RILEY", Last: "RIVERA"}, BirthDate: &commonv1.Date{Year: 2015, Month: 6, Day: 21}}

	// A provider's correction or void is a new submission, so it gets a
	// new number; it links to its original by what adjudication matched.
	tests := []struct {
		name  string
		claim *claimsv1.Claim
		want  string
		// Matched original, by number, or "" for none.
		original string
		match    claimsv1.OriginalMatch
	}{
		{"original", submitted("RIV-88213", "ACM100101", "99213", 180, 12), "26075-E-00002 APPROVED", "", 0},
		{"dependent in patient loop", riley, "26075-E-00003 APPROVED", "", 0},
		{"unknown member", submitted("NGU-30017", "ACM999901", "99213", 180, 13), "26075-E-00004 PENDED MEMBER_NOT_FOUND", "", 0},
		{"correction quoting an old version", correcting(submitted("RIV-87990", "ACM100101", "99214", 250, 3), replacement, "26063-E-00001"),
			"26075-E-00005 APPROVED", "26063-E-00001-2", claimsv1.OriginalMatch_ORIGINAL_MATCH_QUOTED_NUMBER},
		{"correction quoting nothing we issued", correcting(submitted("PAT-39002", "ACM200102", "99213", 180, 9), replacement, "26040-E-00417"),
			"26075-E-00006 PENDED ORIGINAL_NOT_FOUND", "", claimsv1.OriginalMatch_ORIGINAL_MATCH_NONE},
		{"correction of a pended claim", correcting(submitted("RIV-88102", "ACM100102", "70551", 900, 6), replacement, "26068-E-00001"),
			"26075-E-00007 PENDED ORIGINAL_NOT_FINALIZED", "26068-E-00001", claimsv1.OriginalMatch_ORIGINAL_MATCH_QUOTED_NUMBER},
		{"void of a paid claim", correcting(submitted("PAT-40388", "ACM200101", "99214", 250, 4), void, "26064-E-00001"),
			"26075-E-00008 VOID", "26064-E-00001", claimsv1.OriginalMatch_ORIGINAL_MATCH_QUOTED_NUMBER},
	}
	for _, tc := range tests {
		got := receive(t, s, tc.claim)
		if describe(got) != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, describe(got), tc.want)
		}
		orig := ""
		if id := got.GetOriginalClaimId(); id != "" {
			o, _ := s.Repos.Claims.Get(context.Background(), id)
			orig = claims.DisplayNumber(o)
		}
		if orig != tc.original || got.GetAdjudication().GetOriginalMatch() != tc.match {
			t.Errorf("%s: original %q (%s), want %q (%s)", tc.name, orig, got.GetAdjudication().GetOriginalMatch(), tc.original, tc.match)
		}
	}

	// The dependent was matched without rewriting the member id as sent.
	if d := find(t, s, "26075-E-00003"); d.GetMemberId() != "mem_1003" || d.GetMemberCardId() != "ACM100101" {
		t.Errorf("dependent: member %q, member id as sent %q", d.GetMemberId(), d.GetMemberCardId())
	}
	// The correction matched the current version, not the one quoted. That
	// version's result was never reported, so it is closed with an answer
	// of its own, not superseded.
	if v2 := find(t, s, "26063-E-00001-2"); describe(v2) != "26063-E-00001-2 DENIED" || v2.GetSupersededBy() != nil ||
		v2.GetLines()[0].GetAdjudication().GetDenial().GetCode() != "REPLACED_BY_PROVIDER" {
		t.Errorf("26063-E-00001-2 is %s, superseded by %v", describe(v2), v2.GetSupersededBy())
	}
	if o := find(t, s, "26064-E-00001"); o.GetSupersededBy().GetFrequency() != void {
		t.Errorf("26064-E-00001 superseded by %v", o.GetSupersededBy())
	}
	// Customer comes from the matched member.
	if c := find(t, s, "26075-E-00002"); c.GetCustomerId() != sim.CustomerID {
		t.Errorf("customer %q", c.GetCustomerId())
	}
}

// Every run matches from the submitted fields, so a rerun after
// enrollment catches up finds the member. Run 1's record is unchanged.
func TestRerunRematchesMember(t *testing.T) {
	s := sandbox(t)
	c := receive(t, s, submitted("NGU-30017", "ACM999901", "99213", 180, 13))
	if c.GetMemberId() != "" {
		t.Fatalf("matched %q before enrollment", c.GetMemberId())
	}
	m, err := s.Repos.Members.Get(context.Background(), "mem_2002")
	if err != nil {
		t.Fatal(err)
	}
	m.Id, m.SubscriberId, m.CardId, m.Audit = "mem_5001", "mem_5001", "ACM999901", nil
	m.Coverage[0].Id = "cov_5001_med_2026"
	if _, err := s.Repos.Members.Create(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if res := s.Apply(sim.Action{Type: "reprocess", ClaimID: c.GetId(), Reason: "enrollment loaded"}); !res.OK {
		t.Fatal(res.Error)
	}
	c = find(t, s, claims.DisplayNumber(c))
	if describe(c) != "26075-E-00002 APPROVED" || c.GetMemberId() != "mem_5001" || c.GetCustomerId() != sim.CustomerID {
		t.Errorf("after: %s member %q customer %q", c.GetStatus(), c.GetMemberId(), c.GetCustomerId())
	}
	if got := c.GetAdjudicationHistory()[0].GetMemberId(); got != "" {
		t.Errorf("run 1 recorded member %q", got)
	}
}

// A correction linked after it was numbered keeps its own number; the
// link is a relationship, and adjudicating still replaces the original.
func TestLateLinkKeepsNumber(t *testing.T) {
	s := sandbox(t)
	ctx := context.Background()
	c := receive(t, s, correcting(submitted("OKA-10552", "ACM300101", "99214", 250, 2), replacement, "26040-E-00417"))
	if describe(c) != "26075-E-00002 PENDED ORIGINAL_NOT_FOUND" {
		t.Fatalf("got %s", describe(c))
	}
	// An examiner establishes it corrects 26062-E-00001. Linking by hand
	// is not an action yet; set the input directly.
	orig := find(t, s, "26062-E-00001")
	c.OriginalLink = &claimsv1.ClaimLink{ClaimId: orig.GetId(), LinkedBy: "examiner"}
	if _, err := s.Repos.Claims.Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	if res := s.Apply(sim.Action{Type: "reprocess", ClaimID: c.GetId(), Reason: "original confirmed with provider"}); !res.OK {
		t.Fatal(res.Error)
	}
	c = find(t, s, "26075-E-00002")
	if describe(c) != "26075-E-00002 APPROVED" || c.GetOriginalClaimId() != orig.GetId() ||
		c.GetAdjudication().GetOriginalMatch() != claimsv1.OriginalMatch_ORIGINAL_MATCH_LINKED {
		t.Errorf("after link: %s original %q match %s", describe(c), c.GetOriginalClaimId(), c.GetAdjudication().GetOriginalMatch())
	}
	if o := find(t, s, "26062-E-00001"); o.GetSupersededBy().GetFrequency() != replacement {
		t.Errorf("original superseded by %v", o.GetSupersededBy())
	}
}

// A provider's void of a claim sitting in an unreleased payment run: the
// claim's result was never reported, so it leaves the run and is closed
// with an answer of its own. The void has nothing to reverse.
func TestVoidOfClaimInUnreleasedRun(t *testing.T) {
	s := sandbox(t)
	if res := s.Apply(sim.Action{Type: "select", ClaimID: "26065-E-00001"}); !res.OK {
		t.Fatal(res.Error)
	}
	v := receive(t, s, correcting(submitted("RIV-88300", "ACM100101", "70551", 900, 5), void, "26065-E-00001"))
	if describe(v) != "26075-E-00002 VOID" {
		t.Fatalf("got %s", describe(v))
	}
	o := find(t, s, "26065-E-00001")
	if describe(o) != "26065-E-00001 DENIED" || o.GetPayment() != nil || o.GetSupersededBy() != nil ||
		o.GetLines()[0].GetAdjudication().GetDenial().GetCode() != "VOIDED_BY_PROVIDER" {
		t.Errorf("original is %s, payment %v, superseded by %v", describe(o), o.GetPayment(), o.GetSupersededBy())
	}
	// Nothing of the original's is left on the family's accumulators.
	acc, err := s.Repos.FamilyAccumulators.Get(context.Background(), o.GetAdjudicationHistory()[0].GetFamilyAccumulatorsId())
	if err != nil {
		t.Fatal(err)
	}
	var net int64
	for _, e := range acc.GetEntries() {
		if e.GetClaimId() == o.GetId() {
			net += e.GetAmount().GetCents()
		}
	}
	if net != 0 {
		t.Errorf("the original still has %d cents posted", net)
	}
}
