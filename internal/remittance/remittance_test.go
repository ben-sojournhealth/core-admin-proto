package remittance_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	gproto "google.golang.org/protobuf/proto"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	planv1 "coreadmin/gen/coreadmin/plan/v1"
	remittancev1 "coreadmin/gen/coreadmin/remittance/v1"
	"coreadmin/internal/journey"
	"coreadmin/internal/remittance"
	"coreadmin/internal/sim"
	"coreadmin/internal/x12"
)

const (
	processed = remittancev1.RemittanceClaimStatus_REMITTANCE_CLAIM_STATUS_PROCESSED
	denied    = remittancev1.RemittanceClaimStatus_REMITTANCE_CLAIM_STATUS_DENIED
	reversal  = remittancev1.RemittanceClaimStatus_REMITTANCE_CLAIM_STATUS_REVERSAL
)

func setup(t *testing.T) (*sim.Sim, *remittance.Builder) {
	t.Helper()
	s, err := journey.New()
	if err != nil {
		t.Fatal(err)
	}
	return s, &remittance.Builder{
		Repos: s.Repos, ContactName: "Provider Services", ContactPhone: "(937) 555-0100",
		Now: func() time.Time { return time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC) },
	}
}

// receive adjudicates a one-line claim from Dr. Chen's practice.
func receive(t *testing.T, s *sim.Sim, pcn, card, procedure string, dollars int64) *claimsv1.Claim {
	t.Helper()
	dos := &commonv1.DateRange{Start: &commonv1.Date{Year: 2026, Month: 3, Day: 12}, End: &commonv1.Date{Year: 2026, Month: 3, Day: 12}}
	c, err := s.Claims.Receive(context.Background(), &claimsv1.Claim{
		Type: claimsv1.ClaimType_CLAIM_TYPE_PROFESSIONAL, Source: claimsv1.ClaimSource_CLAIM_SOURCE_EDI_837,
		PatientControlNumber: pcn, MemberCardId: card,
		BillingProvider: &claimsv1.ProviderRef{Npi: "1234567893", Tin: "311234567", Name: "Dayton Family Medicine LLC"},
		ServiceDates:    dos, TotalCharge: &commonv1.Money{Cents: dollars * 100}, DiagnosisCodes: []string{"J069"},
		Lines: []*claimsv1.ClaimLine{{LineNumber: 1, ProcedureCode: procedure, PlaceOfServiceCode: "11", UnitsMilli: 1000,
			ServiceDates: dos, Charge: &commonv1.Money{Cents: dollars * 100}, DiagnosisPointers: []int32{1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func check(n string) *remittancev1.RemittancePayment {
	return &remittancev1.RemittancePayment{
		Method: remittancev1.PaymentMethod_PAYMENT_METHOD_CHECK, TraceNumber: n,
		Date: &commonv1.Date{Year: 2026, Month: 3, Day: 20},
	}
}

func env() remittance.Envelope {
	return remittance.Envelope{Sender: "COREADMIN", Receiver: "CLEARPATH", Control: "000000501",
		Time: time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)}
}

// segments renders the 835, checks its envelope parses, and returns its
// segments as text.
func segments(t *testing.T, r *remittancev1.Remittance) []string {
	t.Helper()
	raw := remittance.X12(r, env())
	ic, err := x12.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("835 does not parse: %v\n%s", err, raw)
	}
	set := ic.Groups[0].Sets[0]
	if len(set.Problems) > 0 {
		t.Fatalf("835 envelope: %v\n%s", set.Problems, raw)
	}
	var out []string
	for _, s := range ic.Segments {
		out = append(out, s.String(ic.Delims))
	}
	return out
}

func hasAll(t *testing.T, segs []string, want ...string) {
	t.Helper()
	all := strings.Join(segs, "\n")
	for _, w := range want {
		if !strings.Contains(all+"\n", w+"\n") {
			t.Errorf("835 lacks %s\n%s", w, all)
		}
	}
}

// A paid claim: the CLP, the patient, the service line with its CAS
// breakdown, and a payment equal to what the claim pays.
func TestPaidClaim(t *testing.T) {
	s, b := setup(t)
	c := receive(t, s, "RIV-88213", "ACM100101", "99213", 180)
	la := c.GetLines()[0].GetAdjudication()
	if c.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_APPROVED || la.GetPaid().GetCents() == 0 {
		t.Fatalf("setup: %s, paid %d", c.GetStatus(), la.GetPaid().GetCents())
	}
	r, err := b.Build(context.Background(), []*claimsv1.Claim{c}, check("100231"))
	if err != nil {
		t.Fatal(err)
	}
	e := r.GetClaims()[0]
	if e.GetStatus() != processed || e.GetPaid().GetCents() != la.GetPaid().GetCents() ||
		r.GetPayment().GetAmount().GetCents() != la.GetPaid().GetCents() ||
		e.GetPatientResponsibility().GetCents() != la.GetMemberResponsibility().GetCents() {
		t.Errorf("entry %v, payment %v", e, r.GetPayment())
	}
	// 180 charged, 95 allowed: 85 written off (CO-45), the rest split
	// between the member's cost share (PR) and what we pay.
	segs := segments(t, r)
	hasAll(t, segs,
		"BPR*I*"+amtOf(la.GetPaid())+"*C*CHK************20260320",
		"TRN*1*100231*1831234567",
		"N1*PR*ACME MANUFACTURING",
		"PER*BL*PROVIDER SERVICES*TE*9375550100",
		"N1*PE*DAYTON FAMILY MEDICINE LLC*XX*1234567893",
		"REF*TJ*311234567",
		"CLP*RIV-88213*1*180*"+amtOf(la.GetPaid())+"*"+amtOf(la.GetMemberResponsibility())+"*CI*"+c.GetClaimNumber()+"*11*1",
		"NM1*QC*1*RIVERA*JORDAN****MI*ACM100101",
		"SVC*HC:99213*180*"+amtOf(la.GetPaid()),
		"DTM*472*20260312",
		"AMT*B6*95",
	)
	if !strings.Contains(strings.Join(segs, "\n"), "CAS*CO*45*85") {
		t.Errorf("no contractual write-off:\n%s", strings.Join(segs, "\n"))
	}
}

// A denied claim pays nothing, so the 835 is a notification: BPR*H, NON.
func TestDeniedClaimIsNotificationOnly(t *testing.T) {
	s, b := setup(t)
	ctx := context.Background()
	// The plan denies services no benefit covers; 99499 is one.
	plan, err := s.Repos.Plans.Get(ctx, "plan_acme_ppo")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range plan.GetVersions() {
		v.DefaultBehavior = planv1.DefaultBenefitBehavior_DEFAULT_BENEFIT_BEHAVIOR_DENY
	}
	if _, err := s.Repos.Plans.Update(ctx, plan); err != nil {
		t.Fatal(err)
	}
	c := receive(t, s, "OKA-1", "ACM100101", "99499", 120)
	if c.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_DENIED {
		t.Fatalf("setup: claim is %s, not denied", c.GetStatus())
	}
	r, err := b.Build(ctx, []*claimsv1.Claim{c}, check("100232"))
	if err != nil {
		t.Fatal(err)
	}
	if r.GetPayment().GetMethod() != remittancev1.PaymentMethod_PAYMENT_METHOD_NONE || r.GetClaims()[0].GetStatus() != denied {
		t.Errorf("payment %v, status %s", r.GetPayment(), r.GetClaims()[0].GetStatus())
	}
	hasAll(t, segments(t, r), "BPR*H*0*C*NON************20260320",
		"CLP*OKA-1*4*120*0*"+amtOf(r.GetClaims()[0].GetPatientResponsibility())+"*CI*"+c.GetClaimNumber()+"*11*1",
		"CAS*PR*96*120", "LQ*HE*N130")

	// Without one of its remark codes, CARC 96 is refused.
	for _, rc := range [][]string{nil, {"M15"}} {
		r.GetClaims()[0].GetLines()[0].RemarkCodes = rc
		if err := remittance.Check(r); err == nil || !strings.Contains(err.Error(), "CARC 96 needs one of the remark codes N130") {
			t.Errorf("96 with remarks %v: %v", rc, err)
		}
	}
}

// A replacement reverses exactly what the provider was told about the
// original, then reports the new decision; the payment is the
// difference.
func TestReplacementReversesWhatWasReported(t *testing.T) {
	s, b := setup(t)
	ctx := context.Background()
	orig := receive(t, s, "RIV-88213", "ACM100101", "99213", 180)
	sent, err := b.Build(ctx, []*claimsv1.Claim{orig}, check("100231"))
	if err != nil {
		t.Fatal(err)
	}
	if sent, err = b.Save(ctx, sent); err != nil {
		t.Fatal(err)
	}
	reported := sent.GetClaims()[0]

	// The corrected claim bills a higher-level visit.
	corrected := clone(orig)
	corrected.Lines[0].ProcedureCode = "99214"
	adj, err := s.Claims.AdjustClaim(ctx, &claimsv1.AdjustClaimRequest{OriginalClaimId: orig.GetId(), CorrectedClaim: corrected, Reason: "corrected E/M level"})
	if err != nil {
		t.Fatal(err)
	}
	repl := adj.GetClaim()
	r, err := b.Build(ctx, []*claimsv1.Claim{repl}, check("100377"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.GetClaims()) != 2 {
		t.Fatalf("%d entries, want the reversal and the replacement", len(r.GetClaims()))
	}
	rev, now := r.GetClaims()[0], r.GetClaims()[1]
	if rev.GetStatus() != reversal || rev.GetClaimNumber() != reported.GetClaimNumber() || rev.GetReversesRemittanceId() != sent.GetId() ||
		rev.GetPaid().GetCents() != -reported.GetPaid().GetCents() || rev.GetCharge().GetCents() != -reported.GetCharge().GetCents() {
		t.Errorf("reversal %v", rev)
	}
	if now.GetStatus() != processed || now.GetClaimNumber() != orig.GetClaimNumber()+"-2" || now.GetFrequencyCode() != "7" {
		t.Errorf("replacement entry %v", now)
	}
	if got, want := r.GetPayment().GetAmount().GetCents(), repl.GetAdjudication().GetTotalPaid().GetCents()-reported.GetPaid().GetCents(); got != want {
		t.Errorf("payment %d, want the difference %d", got, want)
	}
	segs := segments(t, r)
	hasAll(t, segs,
		"CLP*RIV-88213*22*-180*-"+amtOf(reported.GetPaid())+"*-"+amtOf(reported.GetPatientResponsibility())+"*CI*"+orig.GetClaimNumber()+"*11*1",
		"SVC*HC:99213*-180*-"+amtOf(reported.GetPaid()),
		"CAS*CO*45*-85",
		"AMT*B6*-95",
		"SVC*HC:99214*180*"+amtOf(repl.GetLines()[0].GetAdjudication().GetPaid()),
	)

	// Once the reversal is sent, the original is not reversed again.
	if _, err := b.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	another := clone(repl)
	another.Id = "another-replacement"
	r2, err := b.Build(ctx, []*claimsv1.Claim{another}, check("100400"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.GetClaims()) != 1 || r2.GetClaims()[0].GetStatus() == reversal {
		t.Errorf("reversed twice: %v", r2.GetClaims())
	}
}

// A replacement of a version that was never reported (adjusted before
// payment) has nothing to reverse.
func TestReplacementOfUnreportedClaim(t *testing.T) {
	s, b := setup(t)
	ctx := context.Background()
	orig := receive(t, s, "RIV-88213", "ACM100101", "99213", 180)
	corrected := clone(orig)
	corrected.Lines[0].ProcedureCode = "99214"
	adj, err := s.Claims.AdjustClaim(ctx, &claimsv1.AdjustClaimRequest{OriginalClaimId: orig.GetId(), CorrectedClaim: corrected, Reason: "corrected"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := b.Build(ctx, []*claimsv1.Claim{adj.GetClaim()}, check("100377"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.GetClaims()) != 1 || r.GetClaims()[0].GetStatus() != processed {
		t.Errorf("entries %v", r.GetClaims())
	}
}

// Voiding a paid claim takes the money back. Recoveries are reported as a
// provider-level adjustment against a later payment, which isn't built:
// until then it is refused rather than sent as a negative payment.
func TestVoidOfReportedClaimNeedsRecovery(t *testing.T) {
	s, b := setup(t)
	ctx := context.Background()
	orig := receive(t, s, "RIV-88213", "ACM100101", "99213", 180)
	sent, _ := b.Build(ctx, []*claimsv1.Claim{orig}, check("100231"))
	if _, err := b.Save(ctx, sent); err != nil {
		t.Fatal(err)
	}
	v, err := s.Claims.VoidClaim(ctx, &claimsv1.VoidClaimRequest{ClaimId: orig.GetId(), Reason: "billed in error"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Build(ctx, []*claimsv1.Claim{v.GetVoidClaim()}, check("100378")); !errors.Is(err, remittance.ErrNetNegative) {
		t.Errorf("err = %v, want ErrNetNegative", err)
	}
	// Alongside a larger payment it nets out.
	other := receive(t, s, "RIV-90001", "ACM100101", "27447", 25000)
	r, err := b.Build(ctx, []*claimsv1.Claim{v.GetVoidClaim(), other}, check("100379"))
	if err != nil {
		t.Fatal(err)
	}
	if r.GetClaims()[0].GetStatus() != reversal || len(r.GetClaims()) != 2 ||
		r.GetPayment().GetAmount().GetCents() != other.GetAdjudication().GetTotalPaid().GetCents()-sent.GetClaims()[0].GetPaid().GetCents() {
		t.Errorf("entries %v, payment %v", r.GetClaims(), r.GetPayment())
	}
}

func TestPreview(t *testing.T) {
	s, b := setup(t)
	ctx := context.Background()
	c := receive(t, s, "RIV-88213", "ACM100101", "99213", 180)
	r, err := b.Preview(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if !r.GetPreview() || r.GetPayment().GetTraceNumber() != "PREVIEW" {
		t.Errorf("preview %v", r.GetPayment())
	}
	if _, err := b.Save(ctx, r); err == nil {
		t.Error("a preview was saved")
	}

	// A pended claim has no decision to show.
	p := receive(t, s, "ZZ-1", "ZZZ999", "99213", 180)
	if _, err := b.Preview(ctx, p); !errors.Is(err, remittance.ErrNotReportable) {
		t.Errorf("pended preview: %v", err)
	}
}

func TestMixedPayeesRefused(t *testing.T) {
	s, b := setup(t)
	a := receive(t, s, "RIV-88213", "ACM100101", "99213", 180)
	c := clone(a)
	c.BillingProvider.Npi = "1999999992"
	if _, err := b.Build(context.Background(), []*claimsv1.Claim{a, c}, check("1")); !errors.Is(err, remittance.ErrMixed) {
		t.Errorf("err = %v", err)
	}
}

func clone(c *claimsv1.Claim) *claimsv1.Claim { return gproto.Clone(c).(*claimsv1.Claim) }

// amtOf is an amount as the 835 writes it: 65, 65.5, 12.34.
func amtOf(m *commonv1.Money) string {
	c := m.GetCents()
	s := fmt.Sprint(c / 100)
	if c%100 != 0 {
		s = strings.TrimRight(fmt.Sprintf("%s.%02d", s, c%100), "0")
	}
	return s
}
