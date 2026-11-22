package claims_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	"coreadmin/internal/claims"
	"coreadmin/internal/common/store"
)

// failingUpdates fails any Update of one claim, standing in for a crash or
// a database error partway through processing.
type failingUpdates struct {
	store.Store[*claimsv1.Claim]
	id string
}

var errDisk = errors.New("disk on fire")

func (f failingUpdates) Update(ctx context.Context, c *claimsv1.Claim) (*claimsv1.Claim, error) {
	if c.GetId() == f.id {
		return nil, errDisk
	}
	return f.Store.Update(ctx, c)
}

// A corrected claim is saved, posts accumulators and retires its original
// in one transaction. If retiring the original fails, none of it happens.
func TestReplacementIsAllOrNothing(t *testing.T) {
	s := sandbox(t)
	ctx := context.Background()
	orig := find(t, s, "26063-A-00001-2")
	accID := orig.GetAdjudication().GetFamilyAccumulatorsId()
	accBefore, err := s.Repos.FamilyAccumulators.Get(ctx, accID)
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 10_000})

	real := s.Repos.Claims
	s.Repos.Claims = failingUpdates{real, orig.GetId()}
	in := correcting(submitted("PAT-40711", "ACM200101", "99214", 250, 2), replacement, "26063-A-00001")
	if _, err := s.Claims.Receive(ctx, in); !errors.Is(err, errDisk) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	s.Repos.Claims = real

	after, _, _ := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 10_000})
	if len(after) != len(before) {
		t.Errorf("claims: %d before, %d after a failed replacement", len(before), len(after))
	}
	if o := find(t, s, "26063-A-00001-2"); o.GetStatus() != orig.GetStatus() || o.GetAudit().GetRevision() != orig.GetAudit().GetRevision() {
		t.Errorf("original changed: %s rev %d", o.GetStatus(), o.GetAudit().GetRevision())
	}
	accAfter, _ := s.Repos.FamilyAccumulators.Get(ctx, accID)
	if accAfter.GetAudit().GetRevision() != accBefore.GetAudit().GetRevision() || len(accAfter.GetEntries()) != len(accBefore.GetEntries()) {
		t.Errorf("accumulators changed: rev %d -> %d, %d -> %d entries", accBefore.GetAudit().GetRevision(),
			accAfter.GetAudit().GetRevision(), len(accBefore.GetEntries()), len(accAfter.GetEntries()))
	}

	// With the failure gone, the same claim goes through, and nothing
	// from the failed attempt is left over to conflict with it. The failed
	// attempt spent 26075-E-00001: a gap that means a claim didn't load.
	out := receive(t, s, in)
	if got := describe(out); got != "26075-E-00002 APPROVED" || out.GetOriginalClaimId() != find(t, s, "26063-A-00001-2").GetId() {
		t.Errorf("retry: %s", got)
	}
	if o := find(t, s, "26063-A-00001-2"); o.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_ADJUSTED {
		t.Errorf("original after retry: %s", o.GetStatus())
	}
}

// A void that fails partway leaves the original as it was and its
// accumulator postings in place.
func TestVoidIsAllOrNothing(t *testing.T) {
	s := sandbox(t)
	ctx := context.Background()
	orig := find(t, s, "26064-A-00001")
	accID := orig.GetAdjudication().GetFamilyAccumulatorsId()
	accBefore, _ := s.Repos.FamilyAccumulators.Get(ctx, accID)

	real := s.Repos.Claims
	s.Repos.Claims = failingUpdates{real, orig.GetId()}
	_, err := s.Claims.VoidClaim(ctx, &claimsv1.VoidClaimRequest{ClaimId: orig.GetId(), Reason: "billed in error"})
	s.Repos.Claims = real
	if !errors.Is(err, errDisk) {
		t.Fatalf("err = %v", err)
	}
	if o := find(t, s, "26064-A-00001"); o.GetStatus() != orig.GetStatus() {
		t.Errorf("original is %s", o.GetStatus())
	}
	if voids, _, _ := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{Filter: func(c *claimsv1.Claim) bool {
		return c.GetOriginalClaimId() == orig.GetId()
	}}); len(voids) != 0 {
		t.Errorf("a void was saved: %s", describe(voids[0]))
	}
	var accAfter *eligibilityv1.FamilyAccumulators
	accAfter, _ = s.Repos.FamilyAccumulators.Get(ctx, accID)
	if accAfter.GetAudit().GetRevision() != accBefore.GetAudit().GetRevision() {
		t.Errorf("accumulators reversed by a void that didn't happen")
	}
}

// interleave starts another claim at the worst moment: right after the
// first correction has checked its original for a replacement in
// progress, and before it commits.
type interleave struct {
	store.Store[*claimsv1.Claim]
	origID string
	then   func()
	fired  atomic.Bool
}

func (w *interleave) List(ctx context.Context, opts store.ListOptions[*claimsv1.Claim]) ([]*claimsv1.Claim, string, error) {
	out, next, err := w.Store.List(ctx, opts)
	// The check for a replacement in progress is the query that would
	// return a pended replacement of the original.
	probe := &claimsv1.Claim{Id: "probe", OriginalClaimId: w.origID, Status: claimsv1.ClaimStatus_CLAIM_STATUS_PENDED}
	if opts.Filter != nil && opts.Filter(probe) && w.fired.CompareAndSwap(false, true) {
		w.then()
	}
	return out, next, err
}

// Two corrections of the same claim arriving together must not both
// become its open replacement. Both pend here (unknown member), and the
// original is already on payment hold, so neither writes the original:
// only the lock keeps them apart.
//
// The second starts while the first holds the lock on the original. It
// waits for the first to commit, then reads what the first wrote, and
// pends behind it.
func TestRacingCorrectionsOneOpenReplacement(t *testing.T) {
	s := sandbox(t)
	ctx := context.Background()
	orig := find(t, s, "26063-A-00001-2")
	if orig.GetPayment() == nil && orig.GetPaymentHold() == nil {
		orig.PaymentHold = &claimsv1.PaymentHold{Reason: "audit"}
		if _, err := s.Repos.Claims.Update(ctx, orig); err != nil {
			t.Fatal(err)
		}
	}
	a := correcting(submitted("PAT-40711", "ZZZ999", "99214", 250, 2), replacement, "26063-A-00001")
	b := correcting(submitted("PAT-40711", "ZZZ999", "99215", 300, 2), replacement, "26063-A-00001")

	done := make(chan *claimsv1.Claim)
	w := &interleave{Store: s.Repos.Claims, origID: orig.GetId()}
	w.then = func() {
		go func() {
			c, err := s.Claims.Receive(store.WithActor(context.Background(), "clearinghouse"), b)
			if err != nil {
				t.Error(err)
			}
			done <- c
		}()
		// Let it reach the lock the first correction holds.
		time.Sleep(200 * time.Millisecond)
	}
	s.Repos.Claims = w
	first := receive(t, s, a)
	second := <-done
	s.Repos.Claims = w.Store
	if !w.fired.Load() || second == nil {
		t.Fatal("the second claim never ran")
	}

	// The first committed while the second waited, so the first is the
	// replacement in progress; the second, reading after it, pends.
	if got := describe(first); !strings.HasSuffix(got, "PENDED MEMBER_NOT_FOUND") || first.GetOriginalClaimId() != orig.GetId() {
		t.Errorf("first: %s, correcting %s", got, first.GetOriginalClaimId())
	}
	pends := describe(second)
	if !strings.Contains(pends, "ORIGINAL_HAS_OPEN_REPLACEMENT") && !strings.Contains(pends, "ORIGINAL_NOT_FINALIZED") {
		t.Errorf("second: %s", pends)
	}
	open, _, _ := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{Filter: func(c *claimsv1.Claim) bool {
		return c.GetOriginalClaimId() == orig.GetId() && c.GetStatus() == claimsv1.ClaimStatus_CLAIM_STATUS_PENDED &&
			!strings.Contains(describe(c), "ORIGINAL_")
	}})
	if len(open) != 1 {
		t.Errorf("%d replacements of %s in progress, want 1", len(open), claims.DisplayNumber(orig))
	}
}
