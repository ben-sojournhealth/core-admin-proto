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

// describe is "number STATUS pend-codes".
func describe(c *claimsv1.Claim) string {
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
	riley.SubmittedPatient = &claimsv1.SubmittedPatient{CardId: "ACM100101", DependentLoop: true,
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
		{"original", submitted("RIV-88213", "ACM100101", "99213", 180, 12), "26075-E-00001 APPROVED", "", 0},
		{"dependent in patient loop", riley, "26075-E-00002 APPROVED", "", 0},
		{"unknown member", submitted("NGU-30017", "ACM999901", "99213", 180, 13), "26075-E-00003 PENDED MEMBER_NOT_FOUND", "", 0},
		{"correction quoting an old version", correcting(submitted("RIV-87990", "ACM100101", "99214", 250, 3), replacement, "26063-A-00001"),
			"26075-E-00004 APPROVED", "26063-A-00001-2", claimsv1.OriginalMatch_ORIGINAL_MATCH_QUOTED_NUMBER},
		{"correction quoting nothing we issued", correcting(submitted("PAT-39002", "ACM200102", "99213", 180, 9), replacement, "26040-E-00417"),
			"26075-E-00005 PENDED ORIGINAL_NOT_FOUND", "", claimsv1.OriginalMatch_ORIGINAL_MATCH_NONE},
		{"correction of a pended claim", correcting(submitted("RIV-88102", "ACM100102", "70551", 900, 6), replacement, "26068-A-00001"),
			"26075-E-00006 PENDED ORIGINAL_NOT_FINALIZED", "26068-A-00001", claimsv1.OriginalMatch_ORIGINAL_MATCH_QUOTED_NUMBER},
		{"void of a paid claim", correcting(submitted("PAT-40388", "ACM200101", "99214", 250, 4), void, "26064-A-00001"),
			"26075-E-00007 APPROVED", "26064-A-00001", claimsv1.OriginalMatch_ORIGINAL_MATCH_QUOTED_NUMBER},
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
	if d := find(t, s, "26075-E-00002"); d.GetMemberId() != "mem_1003" || d.GetMemberCardId() != "ACM100101" {
		t.Errorf("dependent: member %q, member id as sent %q", d.GetMemberId(), d.GetMemberCardId())
	}
	// The correction replaced the current version, not the one quoted.
	if v2 := find(t, s, "26063-A-00001-2"); v2.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_ADJUSTED {
		t.Errorf("26063-A-00001-2 is %s", v2.GetStatus())
	}
	if o := find(t, s, "26064-A-00001"); o.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_VOIDED {
		t.Errorf("26064-A-00001 is %s", o.GetStatus())
	}
	// Customer comes from the matched member.
	if c := find(t, s, "26075-E-00001"); c.GetCustomerId() != sim.CustomerID {
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
	if c.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_APPROVED || c.GetMemberId() != "mem_5001" || c.GetCustomerId() != sim.CustomerID {
		t.Errorf("after: %s member %q customer %q", c.GetStatus(), c.GetMemberId(), c.GetCustomerId())
	}
	if got := c.GetAdjudicationHistory()[0].GetMemberId(); got != "" {
		t.Errorf("run 1 recorded member %q", got)
	}
}

// A correction linked after it was numbered keeps its own number; the
// link is a relationship, and finalizing still replaces the original.
func TestLateLinkKeepsNumber(t *testing.T) {
	s := sandbox(t)
	ctx := context.Background()
	c := receive(t, s, correcting(submitted("OKA-10552", "ACM300101", "99214", 250, 2), replacement, "26040-E-00417"))
	if describe(c) != "26075-E-00001 PENDED ORIGINAL_NOT_FOUND" {
		t.Fatalf("got %s", describe(c))
	}
	// An examiner establishes it corrects 26062-A-00001. Linking by hand
	// is not an action yet; set the input directly.
	orig := find(t, s, "26062-A-00001")
	c.OriginalLink = &claimsv1.ClaimLink{ClaimId: orig.GetId(), LinkedBy: "examiner"}
	if _, err := s.Repos.Claims.Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	if res := s.Apply(sim.Action{Type: "reprocess", ClaimID: c.GetId(), Reason: "original confirmed with provider"}); !res.OK {
		t.Fatal(res.Error)
	}
	c = find(t, s, "26075-E-00001")
	if describe(c) != "26075-E-00001 APPROVED" || c.GetOriginalClaimId() != orig.GetId() ||
		c.GetAdjudication().GetOriginalMatch() != claimsv1.OriginalMatch_ORIGINAL_MATCH_LINKED {
		t.Errorf("after link: %s original %q match %s", describe(c), c.GetOriginalClaimId(), c.GetAdjudication().GetOriginalMatch())
	}
	if o := find(t, s, "26062-A-00001"); o.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_ADJUSTED {
		t.Errorf("original is %s", o.GetStatus())
	}
}

// A void whose original is in an unreleased payment run pends, and
// reprocessing after the run is released finalizes it.
func TestPendedVoidCompletes(t *testing.T) {
	s := sandbox(t)
	for _, a := range []sim.Action{{Type: "releaseHold", ClaimID: "26072-A-00002"}, {Type: "select", ClaimID: "26072-A-00002"}} {
		if res := s.Apply(a); !res.OK {
			t.Fatal(res.Error)
		}
	}
	v := receive(t, s, correcting(submitted("RIV-88300", "ACM100103", "99214", 250, 12), void, "26072-A-00002"))
	if describe(v) != "26075-E-00001 PENDED ORIGINAL_IN_PAYMENT_RUN" {
		t.Fatalf("got %s", describe(v))
	}
	for _, a := range []sim.Action{{Type: "release"}, {Type: "reprocess", ClaimID: v.GetId(), Reason: "payment run released"}} {
		if res := s.Apply(a); !res.OK {
			t.Fatal(res.Error)
		}
	}
	if v = find(t, s, "26075-E-00001"); describe(v) != "26075-E-00001 APPROVED" || v.GetAdjudication().GetRunNumber() != 2 {
		t.Errorf("void is %s, run %d", describe(v), v.GetAdjudication().GetRunNumber())
	}
	if o := find(t, s, "26072-A-00002"); o.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_VOIDED {
		t.Errorf("original is %s", o.GetStatus())
	}
}
