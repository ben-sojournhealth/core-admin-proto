package claims

import (
	"context"
	"errors"
	"fmt"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	"coreadmin/internal/claims/adjudication"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/common/ids"
	"coreadmin/internal/common/store"
)

// This file stands in for the payment system, which is not modeled yet.
// It does the one thing the claims model needs from it: write payment
// state back onto claims (Claim.payment). Payment runs, remittance
// records and netting of recoveries are simplified to log lines.

// SelectForPayment puts current, unpaid claims into
// the (single, unreleased) payment run. With no ids it selects every
// eligible claim.
func (s *Service) SelectForPayment(ctx context.Context, ids []string) ([]string, error) {
	eligible := func(c *claimsv1.Claim) string {
		switch {
		case !current(c):
			return "is " + standing(c) + ", not an adjudicated claim in force"
		case c.GetPayment() != nil:
			return "already has a payment"
		}
		return ""
	}
	var picked []*claimsv1.Claim
	if len(ids) > 0 {
		for _, id := range ids {
			c, err := s.get(ctx, id)
			if err != nil {
				return nil, err
			}
			if why := eligible(c); why != "" {
				return nil, rejectf("%s cannot be selected for payment: it %s", id, why)
			}
			picked = append(picked, c)
		}
	} else {
		all, _, err := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 10_000})
		if err != nil {
			return nil, err
		}
		for _, c := range all {
			if eligible(c) == "" {
				picked = append(picked, c)
			}
		}
		if len(picked) == 0 {
			return nil, rejectf("no claims are eligible for payment")
		}
	}
	// The run is selected whole or not at all.
	var out []string
	err := s.inTx(ctx, func(ctx context.Context) error {
		for _, c := range picked {
			c.Payment = &claimsv1.ClaimPayment{
				Paid:  c.GetAdjudication().GetTotalPaid(),
				State: claimsv1.PaymentState_PAYMENT_STATE_SELECTED,
			}
			if _, err := s.Repos.Claims.Update(ctx, c); err != nil {
				return err
			}
			out = append(out, c.GetId())
			s.logf(ctx, "%s selected for payment: %s.", s.ref(c), USD(c.GetPayment().GetPaid().GetCents()))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReleasePaymentRun issues payment for every selected claim: the point
// after which a claim can no longer be pulled.
func (s *Service) ReleasePaymentRun(ctx context.Context) ([]string, error) {
	selected, _, err := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 10_000, Where: []store.Cond{store.Eq("PAYMENT_STATE_SELECTED", "payment", "state")}, Filter: func(c *claimsv1.Claim) bool {
		return c.GetPayment().GetState() == claimsv1.PaymentState_PAYMENT_STATE_SELECTED
	}})
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, rejectf("no claims are selected for payment")
	}
	s.remittances++
	remit := fmt.Sprintf("rem_%03d", s.remittances)
	// The run is released whole or not at all.
	var out []string
	err = s.inTx(ctx, func(ctx context.Context) error {
		for i, c := range selected {
			c.Payment.State = claimsv1.PaymentState_PAYMENT_STATE_ISSUED
			c.Payment.PaidDate = dates.FromTime(s.Now())
			c.Payment.RemittanceId = remit
			c.Payment.PaymentTraceNumber = fmt.Sprintf("EFT-%03d-%02d", s.remittances, i+1)
			if _, err := s.Repos.Claims.Update(ctx, c); err != nil {
				return err
			}
			out = append(out, c.GetId())
			line := fmt.Sprintf("%s payment issued: %s on %s.", s.ref(c), USD(c.GetPayment().GetPaid().GetCents()), remit)
			if c.GetOriginalClaimId() != "" {
				if orig, err := s.Repos.Claims.Get(ctx, c.GetOriginalClaimId()); err == nil {
					if held := heldAmount(orig); held > 0 {
						line += fmt.Sprintf(" The remittance also reverses %s paid on %s, so the net to the provider is %s.",
							USD(held), s.ref(orig), USD(c.GetPayment().GetPaid().GetCents()-held))
					}
				}
			}
			s.logf(ctx, "%s", line)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RecordPaymentEvent applies something the bank reports about an issued
// payment: "clear", "return" or "reissue". The claim's status never
// changes.
func (s *Service) RecordPaymentEvent(ctx context.Context, claimID, event string) (*claimsv1.Claim, error) {
	c, err := s.get(ctx, claimID)
	if err != nil {
		return nil, err
	}
	state := c.GetPayment().GetState()
	issued := claimsv1.PaymentState_PAYMENT_STATE_ISSUED
	switch event {
	case "clear":
		if state != issued {
			return nil, rejectf("%s has no issued payment to clear", s.ref(c))
		}
		c.Payment.State = claimsv1.PaymentState_PAYMENT_STATE_CLEARED
		s.logf(ctx, "%s payment cleared. Claim status is unchanged.", s.ref(c))
	case "return":
		if state != issued {
			return nil, rejectf("%s has no issued payment to return", s.ref(c))
		}
		c.Payment.State = claimsv1.PaymentState_PAYMENT_STATE_RETURNED
		s.logf(ctx, "%s payment was returned by the bank; the provider holds nothing. Claim status is unchanged.", s.ref(c))
	case "reissue":
		if state != claimsv1.PaymentState_PAYMENT_STATE_RETURNED && state != claimsv1.PaymentState_PAYMENT_STATE_VOIDED {
			return nil, rejectf("%s has no returned or voided payment to reissue", s.ref(c))
		}
		if !current(c) {
			return nil, rejectf("%s is %s; its payment is not reissued", s.ref(c), standing(c))
		}
		s.remittances++
		c.Payment.State = issued
		c.Payment.PaidDate = dates.FromTime(s.Now())
		c.Payment.PaymentTraceNumber = fmt.Sprintf("EFT-%03d-R", s.remittances)
		s.logf(ctx, "%s payment reissued as %s.", s.ref(c), c.GetPayment().GetPaymentTraceNumber())
	default:
		return nil, rejectf("unknown payment event %q", event)
	}
	return s.Repos.Claims.Update(ctx, c)
}

// ProcessConcurrently simulates claims arriving at the same instant: every
// claim's cost sharing is calculated before any of them is committed, so
// claims for the same family all read the same balances. Each commit after
// the first then fails the accumulator revision check and is recalculated.
func (s *Service) ProcessConcurrently(ctx context.Context, in []*claimsv1.Claim) ([]*claimsv1.Claim, error) {
	var inputs []*claimsv1.Claim
	var calcs []*adjudication.Calculation
	for _, c := range in {
		c = cloneClaim(c)
		if c.GetId() == "" {
			c.Id = ids.NewV7(s.Now())
		}
		s.prepareNew(c)
		calc, err := s.Engine.Calculate(ctx, c, "")
		if err != nil {
			return nil, err
		}
		if _, err := s.number(ctx, calc.Claim); err != nil {
			return nil, err
		}
		s.logf(ctx, "%s calculated against accumulator revision %d: %s.", s.ref(calc.Claim), calc.Revision, describe(calc.Claim))
		inputs, calcs = append(inputs, c), append(calcs, calc)
	}
	var out []*claimsv1.Claim
	for i, calc := range calcs {
		var saved *claimsv1.Claim
		err := s.inTx(ctx, func(ctx context.Context) error {
			var err error
			saved, err = s.commit(ctx, calc, true)
			return err
		})
		if errors.Is(err, store.ErrAlreadyExists) && s.exists(ctx, calc.Claim.GetId()) {
			return nil, rejectf("claim %s already exists", s.ref(calc.Claim))
		}
		if stale(err) {
			s.logf(ctx, "%s: commit rejected. Another claim posted to the family's accumulators first (revision %d is stale). Recalculating.",
				s.ref(calc.Claim), calc.Revision)
			inputs[i].ClaimNumber, inputs[i].ClaimVersion = calc.Claim.GetClaimNumber(), calc.Claim.GetClaimVersion()
			saved, err = s.register(ctx, inputs[i], "")
		}
		if err != nil {
			return nil, err
		}
		out = append(out, saved)
	}
	return out, nil
}
