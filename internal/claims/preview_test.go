package claims_test

import (
	"context"
	"strings"
	"testing"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	"coreadmin/internal/common/repo/repotest"
	"coreadmin/internal/common/store"
	"coreadmin/internal/sim"
)

// A preview of an adjudicated claim runs it as if it had posted nothing
// itself: a claim that put its whole allowed amount toward the deductible
// would otherwise find that deductible met, by itself, and pay.
func TestPreviewReadjudicationLeavesOutItsOwnPostings(t *testing.T) {
	s, err := sim.NewIn(repotest.New(t))
	if err != nil {
		t.Fatal(err)
	}
	s.UseShortIDs(false)
	ctx := store.WithActor(context.Background(), "examiner")
	// Jordan has met none of the deductible; the surgery's $1,200 allowed
	// all goes to it.
	c := receive(t, s, submitted("RIV-50001", "ACM100101", "29881", 2500, 12))
	if describe(c) != "26075-E-00001 APPROVED" || c.GetAdjudication().GetTotalPaid().GetCents() != 0 {
		t.Fatalf("setup: %s pays %d", describe(c), c.GetAdjudication().GetTotalPaid().GetCents())
	}
	before, _ := s.Repos.Claims.Get(ctx, c.GetId())
	r, err := s.Claims.PreviewReadjudication(ctx, &claimsv1.PreviewReadjudicationRequest{ClaimId: c.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	p := r.GetClaim().GetAdjudication()
	if p.GetOutcome() != claimsv1.ClaimOutcome_CLAIM_OUTCOME_APPROVED || p.GetTotalPaid().GetCents() != 0 ||
		p.GetTotalMemberResponsibility().GetCents() != c.GetAdjudication().GetTotalMemberResponsibility().GetCents() {
		t.Errorf("preview: %s pays %d, member %d; the claim stands at pays 0, member %d", p.GetOutcome(), p.GetTotalPaid().GetCents(),
			p.GetTotalMemberResponsibility().GetCents(), c.GetAdjudication().GetTotalMemberResponsibility().GetCents())
	}
	// Nothing was saved.
	after, _ := s.Repos.Claims.Get(ctx, c.GetId())
	if after.GetAudit().GetRevision() != before.GetAudit().GetRevision() {
		t.Errorf("the preview wrote the claim: revision %d -> %d", before.GetAudit().GetRevision(), after.GetAudit().GetRevision())
	}

	// Each preview refuses the other's case.
	if _, err := s.Claims.PreviewAdjustment(ctx, &claimsv1.PreviewAdjustmentRequest{OriginalClaimId: c.GetId()}); err == nil || !strings.Contains(err.Error(), "has not been released") {
		t.Errorf("PreviewAdjustment of an unreported claim: %v", err)
	}
	// Once its payment run is released, the cases swap.
	if _, err := s.Claims.SelectForPayment(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claims.ReleasePaymentRun(ctx); err != nil {
		t.Fatal(err)
	}
	paid := c
	if _, err := s.Claims.PreviewReadjudication(ctx, &claimsv1.PreviewReadjudicationRequest{ClaimId: paid.GetId()}); err == nil || !strings.Contains(err.Error(), "preview an adjustment") {
		t.Errorf("PreviewReadjudication of a reported claim: %v", err)
	}
	if _, err := s.Claims.PreviewAdjustment(ctx, &claimsv1.PreviewAdjustmentRequest{OriginalClaimId: paid.GetId()}); err != nil {
		t.Errorf("PreviewAdjustment of a reported claim: %v", err)
	}
}
