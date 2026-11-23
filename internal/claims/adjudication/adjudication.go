// Package adjudication defines the claims adjudication pipeline as a set
// of small interfaces. The Engine composes them in this order for each
// claim:
//
//  1. EligibilityResolver: who is the member, and what coverage applies
//     on the date of service?
//  2. PlanResolver: which plan version is in force?
//  3. ProviderResolver: who is the billing/rendering provider, and which
//     networks are they in for this TIN on this date?
//  4. TierSelector: which of the plan's network tiers applies?
//  5. per line:
//     a. BenefitMatcher: which service category / benefit covers it?
//     b. Pricer: what is the allowed amount?
//     c. CostShareCalculator: split allowed into deductible, copay,
//     coinsurance and plan paid, given current accumulator balances.
//     d. CasMapper: derive the 835 CAS view; CheckLineBalanced verifies
//     the line adds up.
//  6. Finalize (only when nothing pended): AccumulatorLedger.Post applies
//     the member cost sharing to the family's accumulator record,
//     conditional on that record being at the revision step 5c read. On
//     ErrBalancesChanged the engine takes a new snapshot and repeats 5c.
//     No lock is held at any point: the family record's revision is what
//     keeps two claims for one family from using the same stale balance.
//  7. stoploss.Tracker (outside this package): record plan-paid amounts
//     against the customer's stop-loss policy once the claim is paid.
//
// Any step can stop a claim for manual review by returning a *PendError.
// The Engine records it as a Pend on the claim or line instead of failing.
// A pended claim posts nothing; it is re-run with ReprocessClaim.
//
// The Adjudication written to a claim is output only. No step reads a
// previous result: the ledger, stop-loss and remittance each keep their
// own records by claim id and reverse from those.
package adjudication

import (
	"context"
	"errors"
	"fmt"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	planv1 "coreadmin/gen/coreadmin/plan/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
)

// Engine adjudicates a whole claim. It returns the claim with status,
// adjudication and line adjudications filled in.
//
// The claim must be RECEIVED or PENDED. Finalized claims are never
// re-adjudicated in place; AdjustClaim adjudicates a replacement claim.
type Engine interface {
	Adjudicate(ctx context.Context, claim *claimsv1.Claim, opts Options) (*claimsv1.Claim, error)
}

type Options struct {
	// Reason is recorded on the adjudication run (empty for a first run).
	Reason string
}

// EligibilityResolver finds the member and the coverage span covering
// the date of service.
type EligibilityResolver interface {
	ResolveCoverage(ctx context.Context, claim *claimsv1.Claim, serviceDate *commonv1.Date) (*eligibilityv1.Member, *eligibilityv1.CoverageSpan, error)
}

// PlanResolver returns the plan and the version in force on a date.
type PlanResolver interface {
	ResolvePlan(ctx context.Context, planID string, asOf *commonv1.Date) (*planv1.Plan, *planv1.PlanVersion, error)
}

// ProviderResolver resolves a provider reference from a claim.
type ProviderResolver interface {
	ResolveProvider(ctx context.Context, ref *claimsv1.ProviderRef, serviceDate *commonv1.Date) (*ProviderMatch, error)
}

// ProviderMatch is a resolved provider with the participations in force
// for the billed TIN on the service date.
type ProviderMatch struct {
	Provider       *providerv1.Provider
	Participations []*providerv1.NetworkParticipation
}

// TierSelector picks the network tier (and the participation, if any,
// that put the provider in it).
type TierSelector interface {
	SelectTier(version *planv1.PlanVersion, provider *ProviderMatch) (*planv1.NetworkTier, *providerv1.NetworkParticipation, error)
}

// BenefitMatcher maps a claim line to a service category and its benefit.
type BenefitMatcher interface {
	MatchBenefit(version *planv1.PlanVersion, claim *claimsv1.Claim, line *claimsv1.ClaimLine) (*planv1.ServiceCategory, *planv1.Benefit, error)
}

// Pricer computes the allowed amount for a line. When participation is
// nil the provider is non-contracted and the tier's non_contracted_basis
// applies.
type Pricer interface {
	PriceLine(ctx context.Context, req PriceRequest) (*commonv1.Money, error)
}

type PriceRequest struct {
	Claim         *claimsv1.Claim
	Line          *claimsv1.ClaimLine
	Tier          *planv1.NetworkTier
	Participation *providerv1.NetworkParticipation
}

// CostShareCalculator is pure: given allowed, the applicable cost share
// and current balances, it returns the line adjudication and the
// accumulator postings it implies. It does no I/O, so it can be
// exhaustively unit-tested against plan designs.
type CostShareCalculator interface {
	Calculate(req CostShareRequest) (*claimsv1.LineAdjudication, []*claimsv1.AccumulatorPosting, error)
}

type CostShareRequest struct {
	Line      *claimsv1.ClaimLine
	Allowed   *commonv1.Money
	CostShare *planv1.CostShare
	Version   *planv1.PlanVersion
	TierID    string
	// MemberID and SubscriberID identify which balances in Accumulators
	// apply (individual and family).
	MemberID     string
	SubscriberID string
	// Accumulators is the engine's working copy of the family record. The
	// calculator adds this line's cost sharing to it, so later lines on
	// the same claim see earlier lines' amounts. It is never saved; only
	// AccumulatorLedger.Post changes stored balances.
	Accumulators *claimsv1.FamilyAccumulators
	LineNumber   int32
}

// CasMapper derives the 835 CAS adjustments for a line from its
// breakdown (pricing, cost shares, denial). Pure.
type CasMapper interface {
	DeriveCAS(line *claimsv1.LineAdjudication) []*claimsv1.CasAdjustment
}

// AccumulatorLedger reads and writes accumulator balances. It is the only
// channel through which one claim's outcome affects another's.
type AccumulatorLedger interface {
	// Snapshot returns the family's accumulator record for the plan's
	// benefit period containing AsOf (an empty record at revision 0 if
	// nothing has posted yet). The record's audit.revision is what Post
	// must be given.
	Snapshot(ctx context.Context, req SnapshotRequest) (*claimsv1.FamilyAccumulators, error)
	// Post applies a claim's postings in one conditional write. Called
	// exactly once per claim, when it is adjudicated. If the family record
	// is no longer at req.Revision, nothing is applied and Post returns
	// ErrBalancesChanged. Idempotent per claim id, so a retry after a
	// crash cannot double-post.
	Post(ctx context.Context, req PostRequest) error
	// Reverse posts the negation of everything an adjudicated claim posted,
	// from the family record's own entries for that claim. Used by
	// VoidClaim. (AdjustClaim reverses through PostRequest.ReverseClaimID
	// so the reversal and the replacement's postings are one write.)
	Reverse(ctx context.Context, familyAccumulatorsID, claimID string) error
}

type SnapshotRequest struct {
	Plan   *planv1.Plan
	Member *eligibilityv1.Member
	AsOf   *commonv1.Date
	// ExcludeClaimID returns balances as if this claim had never posted.
	// Used to calculate a replacement claim before its original is
	// reversed. The stored record is not changed.
	ExcludeClaimID string
}

type PostRequest struct {
	FamilyAccumulatorsID string
	// Revision is the audit.revision of the snapshot the postings were
	// calculated against (0 if the record did not exist yet).
	Revision int64
	// New is the record header (id, customer, subscriber, plan, benefit
	// period) used to create the record on the family's first posting.
	New *claimsv1.FamilyAccumulators
	// Limits is each accumulator's limit on the plan version used, by
	// accumulator id; copied onto balances for display.
	Limits      map[string]*commonv1.Money
	ClaimID     string
	ServiceDate *commonv1.Date
	Postings    []*claimsv1.AccumulatorPosting
	// ReverseClaimID, when set, reverses that claim's postings in the same
	// write (the original of a replacement claim).
	ReverseClaimID string
}

// ErrBalancesChanged is returned by AccumulatorLedger.Post when another
// claim posted to the same balances after cost sharing was calculated.
var ErrBalancesChanged = errors.New("adjudication: accumulator balances changed since calculation")

// PendError stops adjudication of a claim (LineNumber 0) or a line for
// manual review. It is not a system failure.
type PendError struct {
	Code       string
	Message    string
	LineNumber int32
}

func (e *PendError) Error() string {
	return fmt.Sprintf("pend %s: %s", e.Code, e.Message)
}

// Pend constructs a claim-level PendError.
func Pend(code, format string, args ...any) *PendError {
	return &PendError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// AsPend reports whether err is (or wraps) a PendError.
func AsPend(err error) (*PendError, bool) {
	var p *PendError
	ok := errors.As(err, &p)
	return p, ok
}

// Well-known pend codes.
const (
	PendNoEligibility     = "NO_ELIGIBILITY"
	PendCoverageInGrace   = "COVERAGE_IN_GRACE"
	PendProviderNotFound  = "PROVIDER_NOT_FOUND"
	PendProviderSanction  = "PROVIDER_SANCTIONED"
	PendNoBenefitMatch    = "NO_BENEFIT_MATCH"
	PendPriorAuthRequired = "PRIOR_AUTH_REQUIRED"
	PendNoPrice           = "NO_PRICE"
	PendHighDollar        = "HIGH_DOLLAR"
)
