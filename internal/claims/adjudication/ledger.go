package adjudication

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/common/store"
)

// StoreLedger is the AccumulatorLedger backed by one FamilyAccumulators
// record per subscriber unit, plan and benefit period. All of its safety
// comes from the store's revision check; it holds no locks.
type StoreLedger struct {
	Records store.Store[*eligibilityv1.FamilyAccumulators]
	Now     func() time.Time
}

var _ AccumulatorLedger = (*StoreLedger)(nil)

// FamilyAccumulatorsID is the deterministic record id.
func FamilyAccumulatorsID(subscriberID, planID string, periodStart *commonv1.Date) string {
	return fmt.Sprintf("%s:%s:%s", subscriberID, planID, dates.Format(periodStart))
}

func (l *StoreLedger) Snapshot(ctx context.Context, req SnapshotRequest) (*eligibilityv1.FamilyAccumulators, error) {
	period := req.Plan.GetBenefitPeriod()
	id := FamilyAccumulatorsID(req.Member.GetSubscriberId(), req.Plan.GetId(), period.GetStart())
	rec, err := l.Records.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		rec = &eligibilityv1.FamilyAccumulators{
			Id:            id,
			CustomerId:    req.Member.GetCustomerId(),
			SubscriberId:  req.Member.GetSubscriberId(),
			PlanId:        req.Plan.GetId(),
			BenefitPeriod: period,
		}
	} else if err != nil {
		return nil, err
	}
	if req.ExcludeClaimID != "" {
		for key, net := range netByBalance(rec, req.ExcludeClaimID) {
			bump(rec, key.accumulatorID, key.ownerID, -net, nil)
		}
		kept := rec.Entries[:0]
		for _, e := range rec.GetEntries() {
			if e.GetClaimId() != req.ExcludeClaimID {
				kept = append(kept, e)
			}
		}
		rec.Entries = kept
	}
	return rec, nil
}

func (l *StoreLedger) Post(ctx context.Context, req PostRequest) error {
	rec, err := l.Records.Get(ctx, req.FamilyAccumulatorsID)
	exists := err == nil
	if errors.Is(err, store.ErrNotFound) {
		if req.New == nil {
			return fmt.Errorf("adjudication: no accumulator record %s and no header to create it", req.FamilyAccumulatorsID)
		}
		rec = proto.Clone(req.New).(*eligibilityv1.FamilyAccumulators)
		rec.Balances, rec.Entries, rec.Audit = nil, nil, nil
	} else if err != nil {
		return err
	}

	// Idempotent: a retry after the write succeeded changes nothing.
	for _, e := range rec.GetEntries() {
		if e.GetClaimId() == req.ClaimID {
			return nil
		}
	}
	if rec.GetAudit().GetRevision() != req.Revision {
		return fmt.Errorf("%w: %s is at revision %d, calculated against %d",
			ErrBalancesChanged, rec.GetId(), rec.GetAudit().GetRevision(), req.Revision)
	}

	now := timestamppb.New(l.Now())
	changed := false
	if req.ReverseClaimID != "" {
		changed = reverseEntries(rec, req.ReverseClaimID, now)
	}
	for _, p := range req.Postings {
		if p.GetAmount().GetCents() == 0 {
			continue
		}
		rec.Entries = append(rec.Entries, &eligibilityv1.AccumulatorEntry{
			AccumulatorId:   p.GetAccumulatorId(),
			OwnerId:         p.GetOwnerId(),
			ClaimId:         req.ClaimID,
			ClaimLineNumber: p.GetLineNumber(),
			Amount:          p.GetAmount(),
			ServiceDate:     req.ServiceDate,
			PostedTime:      now,
		})
		bump(rec, p.GetAccumulatorId(), p.GetOwnerId(), p.GetAmount().GetCents(), req.Limits[p.GetAccumulatorId()])
		changed = true
	}
	if !changed {
		return nil
	}
	return l.save(ctx, rec, exists)
}

func (l *StoreLedger) Reverse(ctx context.Context, familyAccumulatorsID, claimID string) error {
	// Read-modify-write; on a concurrent write, re-read and try again.
	for range 5 {
		rec, err := l.Records.Get(ctx, familyAccumulatorsID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if !reverseEntries(rec, claimID, timestamppb.New(l.Now())) {
			return nil
		}
		err = l.save(ctx, rec, true)
		if !errors.Is(err, ErrBalancesChanged) {
			return err
		}
	}
	return ErrBalancesChanged
}

func (l *StoreLedger) save(ctx context.Context, rec *eligibilityv1.FamilyAccumulators, exists bool) error {
	var err error
	if exists {
		_, err = l.Records.Update(ctx, rec)
	} else {
		_, err = l.Records.Create(ctx, rec)
	}
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrAlreadyExists) {
		return fmt.Errorf("%w: %s", ErrBalancesChanged, rec.GetId())
	}
	return err
}

type balanceKey struct{ accumulatorID, ownerID string }

// netByBalance sums a claim's entries per balance (zero once reversed).
func netByBalance(rec *eligibilityv1.FamilyAccumulators, claimID string) map[balanceKey]int64 {
	net := map[balanceKey]int64{}
	for _, e := range rec.GetEntries() {
		if e.GetClaimId() == claimID {
			net[balanceKey{e.GetAccumulatorId(), e.GetOwnerId()}] += e.GetAmount().GetCents()
		}
	}
	for k, v := range net {
		if v == 0 {
			delete(net, k)
		}
	}
	return net
}

// reverseEntries appends entries negating everything claimID has posted.
// Reports whether anything changed.
func reverseEntries(rec *eligibilityv1.FamilyAccumulators, claimID string, now *timestamppb.Timestamp) bool {
	net := netByBalance(rec, claimID)
	// Keep entry order stable: walk existing entries rather than the map.
	done := map[balanceKey]bool{}
	for _, e := range rec.GetEntries() {
		k := balanceKey{e.GetAccumulatorId(), e.GetOwnerId()}
		amt, ok := net[k]
		if !ok || done[k] || e.GetClaimId() != claimID {
			continue
		}
		done[k] = true
		rec.Entries = append(rec.Entries, &eligibilityv1.AccumulatorEntry{
			AccumulatorId: k.accumulatorID,
			OwnerId:       k.ownerID,
			ClaimId:       claimID,
			Amount:        &commonv1.Money{Cents: -amt},
			PostedTime:    now,
		})
		bump(rec, k.accumulatorID, k.ownerID, -amt, nil)
	}
	return len(done) > 0
}

// Applied returns the amount applied to one balance.
func Applied(rec *eligibilityv1.FamilyAccumulators, accumulatorID, ownerID string) int64 {
	for _, b := range rec.GetBalances() {
		if b.GetAccumulatorId() == accumulatorID && b.GetOwnerId() == ownerID {
			return b.GetApplied().GetCents()
		}
	}
	return 0
}

// bump adds amount to a balance, creating it if needed.
func bump(rec *eligibilityv1.FamilyAccumulators, accumulatorID, ownerID string, amount int64, limit *commonv1.Money) {
	for _, b := range rec.GetBalances() {
		if b.GetAccumulatorId() == accumulatorID && b.GetOwnerId() == ownerID {
			b.Applied = &commonv1.Money{Cents: b.GetApplied().GetCents() + amount}
			if limit != nil {
				b.Limit = limit
			}
			return
		}
	}
	rec.Balances = append(rec.Balances, &eligibilityv1.AccumulatorBalance{
		AccumulatorId: accumulatorID,
		OwnerId:       ownerID,
		Applied:       &commonv1.Money{Cents: amount},
		Limit:         limit,
	})
}

// postingTotals sums postings per balance, for comparing two results.
func postingTotals(ps []*claimsv1.AccumulatorPosting) map[balanceKey]int64 {
	t := map[balanceKey]int64{}
	for _, p := range ps {
		if c := p.GetAmount().GetCents(); c != 0 {
			t[balanceKey{p.GetAccumulatorId(), p.GetOwnerId()}] += c
		}
	}
	return t
}

// PostingsDiffer reports whether two adjudication results post different
// amounts to any accumulator balance.
func PostingsDiffer(a, b []*claimsv1.AccumulatorPosting) bool {
	ta, tb := postingTotals(a), postingTotals(b)
	if len(ta) != len(tb) {
		return true
	}
	for k, v := range ta {
		if tb[k] != v {
			return true
		}
	}
	return false
}
