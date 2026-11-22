// Package stoploss tracks paid claims against customers' stop-loss
// policies.
package stoploss

import (
	"context"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	stoplossv1 "coreadmin/gen/coreadmin/stoploss/v1"
)

// Tracker updates specific and aggregate exposure as claims are paid,
// adjusted or voided.
type Tracker interface {
	// RecordPayment applies a paid claim to every policy whose contract
	// basis window includes the claim's incurred date and paidDate.
	// Idempotent per claim id.
	RecordPayment(ctx context.Context, claim *claimsv1.Claim, paidDate *commonv1.Date) error
	// ReversePayment removes a claim's contribution (claim adjusted or
	// voided).
	ReversePayment(ctx context.Context, claimID string) error
	// RecordCensus sets a month's subscriber counts by tier, which drive the
	// aggregate attachment point.
	RecordCensus(ctx context.Context, policyID string, month *stoplossv1.MonthlyAggregate) error
}

// Window is the date range a policy's contract basis implies.
type Window struct {
	Incurred *commonv1.DateRange
	Paid     *commonv1.DateRange
}

// WindowCalculator derives the incurred/paid windows from a policy.
// Pure function, kept as an interface so contract-basis edge cases
// (unlimited run-in, terminal liability) can be swapped.
type WindowCalculator interface {
	Window(policy *stoplossv1.StopLossPolicy) (Window, error)
}
