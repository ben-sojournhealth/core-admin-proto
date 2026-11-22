package adjudication_test

import (
	"context"
	"coreadmin/internal/common/repo/repotest"
	"testing"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	planv1 "coreadmin/gen/coreadmin/plan/v1"
	"coreadmin/internal/claims/adjudication"
	"coreadmin/internal/sim"
)

func TestCheckRemarks(t *testing.T) {
	co45 := &claimsv1.CasAdjustment{ReasonCode: "45"}
	pr96 := &claimsv1.CasAdjustment{ReasonCode: "96"}
	for _, tc := range []struct {
		cas     []*claimsv1.CasAdjustment
		remarks []string
		ok      bool
	}{
		{[]*claimsv1.CasAdjustment{co45}, nil, true},              // no rule for 45
		{[]*claimsv1.CasAdjustment{pr96}, []string{"N130"}, true}, // one of the allowed
		{[]*claimsv1.CasAdjustment{pr96}, []string{"M15", "N130"}, true},
		{[]*claimsv1.CasAdjustment{pr96}, nil, false},             // none
		{[]*claimsv1.CasAdjustment{pr96}, []string{"M15"}, false}, // not an allowed one
	} {
		if err := adjudication.CheckRemarks(tc.cas, tc.remarks); (err == nil) != tc.ok {
			t.Errorf("%v with %v: %v", tc.cas, tc.remarks, err)
		}
	}
}

// A line whose CARC lacks a required remark code pends instead of
// finalizing, and finalizes once a rerun gets the codes right.
func TestMissingRemarkCodePends(t *testing.T) {
	ctx := context.Background()
	s, err := sim.NewIn(repotest.New(t))
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := s.Repos.Plans.Get(ctx, "plan_acme_ppo")
	for _, v := range plan.GetVersions() {
		v.DefaultBehavior = planv1.DefaultBenefitBehavior_DEFAULT_BENEFIT_BEHAVIOR_DENY
	}
	if _, err := s.Repos.Plans.Update(ctx, plan); err != nil {
		t.Fatal(err)
	}
	undo := adjudication.SetDenialRemarks(adjudication.DenyNotCovered, nil)
	defer undo()

	dos := &commonv1.DateRange{Start: &commonv1.Date{Year: 2026, Month: 3, Day: 12}, End: &commonv1.Date{Year: 2026, Month: 3, Day: 12}}
	line := func(n int32, proc string) *claimsv1.ClaimLine {
		return &claimsv1.ClaimLine{LineNumber: n, ProcedureCode: proc, PlaceOfServiceCode: "11", UnitsMilli: 1000, ServiceDates: dos,
			Charge: &commonv1.Money{Cents: 12000}, DiagnosisPointers: []int32{1}}
	}
	c, err := s.Claims.Receive(ctx, &claimsv1.Claim{
		Type: claimsv1.ClaimType_CLAIM_TYPE_PROFESSIONAL, Source: claimsv1.ClaimSource_CLAIM_SOURCE_EDI_837,
		PatientControlNumber: "OKA-1", MemberCardId: "ACM100103", ServiceDates: dos, DiagnosisCodes: []string{"J069"},
		BillingProvider: &claimsv1.ProviderRef{Npi: "1234567893", Tin: "311234567", Name: "Dayton Family Medicine LLC"},
		TotalCharge:     &commonv1.Money{Cents: 24000}, Lines: []*claimsv1.ClaimLine{line(1, "99213"), line(2, "99499")},
	})
	if err != nil {
		t.Fatal(err)
	}
	pends := c.GetAdjudication().GetPends()
	l2 := c.GetLines()[1].GetAdjudication()
	if c.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_PENDED || len(pends) != 1 ||
		pends[0].GetCode() != adjudication.PendRemarkCodeRequired || pends[0].GetLineNumber() != 2 {
		t.Fatalf("status %s, pends %v", c.GetStatus(), pends)
	}
	// The pended line keeps what it would have reported; the other line
	// is decided.
	if l2.GetStatus() != claimsv1.LineStatus_LINE_STATUS_PENDED || l2.GetCasAdjustments()[0].GetReasonCode() != "96" ||
		c.GetLines()[0].GetAdjudication().GetStatus() != claimsv1.LineStatus_LINE_STATUS_PAYABLE {
		t.Errorf("lines: %v / %v", c.GetLines()[0].GetAdjudication().GetStatus(), l2)
	}
	if adjudication.Overridable(adjudication.PendRemarkCodeRequired) {
		t.Error("an examiner can override a missing remark code")
	}

	// The reporting is fixed; the rerun finalizes.
	undo()
	out, err := s.Claims.ReprocessClaim(ctx, &claimsv1.ReprocessClaimRequest{ClaimId: c.GetId(), Reason: "remark codes fixed"})
	if err != nil {
		t.Fatal(err)
	}
	rc := out.GetClaim()
	if rc.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_APPROVED || rc.GetLines()[1].GetAdjudication().GetStatus() != claimsv1.LineStatus_LINE_STATUS_DENIED ||
		rc.GetLines()[1].GetAdjudication().GetRemarkCodes()[0] != "N130" {
		t.Errorf("after rerun: %s, line 2 %v", rc.GetStatus(), rc.GetLines()[1].GetAdjudication())
	}
}
