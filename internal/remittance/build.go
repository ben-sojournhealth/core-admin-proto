// Package remittance builds 835s from adjudicated claims.
//
// A remittance is a document about claims, separate from the money:
// payments decide what is paid and when, and this package reports it. The
// same code builds the 835 sent with a payment and the preview an
// examiner looks at before a claim is finalized; only the payment
// differs.
//
// What a remittance reports comes from each line's adjudication, which is
// already balanced (charge - paid = sum of CAS adjustments; see
// adjudication.CheckLineBalanced). Building it adds the parts that come
// from history: a replacement or void reverses what the provider was told
// about the version it replaces, taken from the stored remittance it was
// reported on.
package remittance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	remittancev1 "coreadmin/gen/coreadmin/remittance/v1"
	"coreadmin/internal/dates"
	"coreadmin/internal/ids"
	"coreadmin/internal/repo"
	"coreadmin/internal/store"
)

var (
	// ErrNotReportable: a claim with no decision to report (pended, or
	// already replaced).
	ErrNotReportable = errors.New("remittance: claim cannot be reported")
	// ErrMixed: claims for different customers or payees, which are
	// different payments.
	ErrMixed = errors.New("remittance: claims belong on different remittances")
	// ErrNetNegative: reversals exceed new payments. That is a recovery,
	// reported as a provider-level adjustment (PLB) offsetting a later
	// payment, which is not built yet.
	ErrNetNegative = errors.New("remittance: payment would be negative")
)

// Builder makes remittances.
type Builder struct {
	Repos *repo.Repos
	// Who providers call about a remittance (PER*BL).
	ContactName, ContactPhone string
	Now                       func() time.Time
}

// Build makes the remittance for one payment to one payee. Every claim
// must be finalized, and all must share a customer and billing provider.
// A replacement brings the reversal of what was reported for the version
// it replaces, right before it; a void is only that reversal. A version
// that was never reported has nothing to reverse.
//
// pay says how the money moves; its amount is set here, from the claims.
// With nothing to pay, the method becomes NONE.
func (b *Builder) Build(ctx context.Context, claims []*claimsv1.Claim, pay *remittancev1.RemittancePayment) (*remittancev1.Remittance, error) {
	if len(claims) == 0 {
		return nil, fmt.Errorf("%w: no claims", ErrNotReportable)
	}
	first := claims[0]
	rem := &remittancev1.Remittance{
		Id:          ids.NewV7(b.Now()),
		CustomerId:  first.GetCustomerId(),
		CreatedTime: timestamppb.New(b.Now()),
		Payee: &remittancev1.Party{
			Name: first.GetBillingProvider().GetName(),
			Npi:  first.GetBillingProvider().GetNpi(),
			Tin:  first.GetBillingProvider().GetTin(),
		},
	}
	cust, err := b.Repos.Customers.Get(ctx, rem.GetCustomerId())
	if err != nil {
		return nil, fmt.Errorf("%w: %s has no matched customer", ErrNotReportable, number(first))
	}
	rem.Payer = &remittancev1.Party{
		Name: cust.GetName(), Tin: cust.GetEin(), Address: cust.GetAddress(),
		ContactName: b.ContactName, ContactPhone: b.ContactPhone,
	}

	for _, c := range claims {
		if c.GetCustomerId() != rem.GetCustomerId() ||
			c.GetBillingProvider().GetNpi() != rem.GetPayee().GetNpi() || c.GetBillingProvider().GetTin() != rem.GetPayee().GetTin() {
			return nil, fmt.Errorf("%w: %s is for %s, billed by %s/%s", ErrMixed, number(c),
				c.GetCustomerId(), c.GetBillingProvider().GetNpi(), c.GetBillingProvider().GetTin())
		}
		if c.GetFrequency() != claimsv1.ClaimFrequency_CLAIM_FREQUENCY_ORIGINAL && c.GetOriginalClaimId() != "" {
			rev, err := b.reversal(ctx, c.GetOriginalClaimId())
			if err != nil {
				return nil, err
			}
			if rev != nil {
				rem.Claims = append(rem.Claims, rev)
			}
		}
		if c.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID {
			continue
		}
		e, err := b.entry(ctx, c)
		if err != nil {
			return nil, err
		}
		rem.Claims = append(rem.Claims, e)
	}

	var total int64
	for _, e := range rem.GetClaims() {
		total += e.GetPaid().GetCents()
	}
	if total < 0 {
		return nil, fmt.Errorf("%w: reversals exceed payments by %d cents", ErrNetNegative, -total)
	}
	rem.Payment = proto.Clone(pay).(*remittancev1.RemittancePayment)
	rem.Payment.Amount = money(total)
	if total == 0 {
		rem.Payment.Method = remittancev1.PaymentMethod_PAYMENT_METHOD_NONE
		rem.Payment.FromAccount, rem.Payment.ToAccount = nil, nil
	}
	if err := Check(rem); err != nil {
		return nil, err
	}
	return rem, nil
}

// Preview builds the remittance one claim would be reported on if it were
// paid today, from its current adjudication or a dry run's. It is never
// stored or sent.
func (b *Builder) Preview(ctx context.Context, c *claimsv1.Claim) (*remittancev1.Remittance, error) {
	rem, err := b.Build(ctx, []*claimsv1.Claim{c}, &remittancev1.RemittancePayment{
		Method: remittancev1.PaymentMethod_PAYMENT_METHOD_CHECK,
		Date:   dates.FromTime(b.Now()), TraceNumber: "PREVIEW",
	})
	if err != nil {
		return nil, err
	}
	rem.Preview = true
	return rem, nil
}

// Save stores a remittance as sent. Later reversals are taken from it.
func (b *Builder) Save(ctx context.Context, rem *remittancev1.Remittance) (*remittancev1.Remittance, error) {
	if rem.GetPreview() {
		return nil, fmt.Errorf("remittance: a preview is never saved")
	}
	return b.Repos.Remittances.Create(ctx, rem)
}

// entry reports one claim's current decision.
func (b *Builder) entry(ctx context.Context, c *claimsv1.Claim) (*remittancev1.RemittanceClaim, error) {
	e := &remittancev1.RemittanceClaim{
		ClaimId: c.GetId(), ClaimNumber: number(c), PatientControlNumber: c.GetPatientControlNumber(),
		FrequencyCode: frequencyCode(c.GetFrequency()), ReceivedDate: dates.FromTime(c.GetReceivedTime().AsTime()),
	}
	switch c.GetStatus() {
	case claimsv1.ClaimStatus_CLAIM_STATUS_APPROVED:
		e.Status = remittancev1.RemittanceClaimStatus_REMITTANCE_CLAIM_STATUS_PROCESSED
	case claimsv1.ClaimStatus_CLAIM_STATUS_DENIED:
		e.Status = remittancev1.RemittanceClaimStatus_REMITTANCE_CLAIM_STATUS_DENIED
	default:
		return nil, fmt.Errorf("%w: %s is %s", ErrNotReportable, number(c), c.GetStatus())
	}

	// The patient as we know them, falling back to as sent.
	e.PatientName, e.PatientMemberId = c.GetSubmittedPatient().GetName(), c.GetMemberCardId()
	if m, err := b.Repos.Members.Get(ctx, c.GetMemberId()); err == nil {
		e.PatientName, e.PatientMemberId = m.GetName(), m.GetCardId()
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if r := c.GetRenderingProvider(); r.GetNpi() != "" && r.GetNpi() != c.GetBillingProvider().GetNpi() {
		e.RenderingProvider = r
	}
	e.FacilityCode = facilityCode(c)

	var charge, paid, pr int64
	for _, l := range c.GetLines() {
		la := l.GetAdjudication()
		if la == nil || la.GetStatus() == claimsv1.LineStatus_LINE_STATUS_PENDED {
			return nil, fmt.Errorf("%w: %s line %d has no decision", ErrNotReportable, number(c), l.GetLineNumber())
		}
		rl := &remittancev1.RemittanceLine{
			LineNumber: l.GetLineNumber(), ProcedureCode: l.GetProcedureCode(), Modifiers: l.GetModifiers(),
			RevenueCode: l.GetRevenueCode(), Charge: la.GetCharge(), Paid: la.GetPaid(), UnitsMilli: l.GetUnitsMilli(),
			ServiceDates: l.GetServiceDates(), RemarkCodes: la.GetRemarkCodes(),
		}
		for _, a := range la.GetCasAdjustments() {
			rl.Adjustments = append(rl.Adjustments, proto.Clone(a).(*claimsv1.CasAdjustment))
			if a.GetGroup() == claimsv1.CasGroup_CAS_GROUP_PATIENT_RESPONSIBILITY {
				pr += a.GetAmount().GetCents()
			}
		}
		if la.GetStatus() == claimsv1.LineStatus_LINE_STATUS_PAYABLE {
			rl.Allowed = la.GetPricing().GetAllowed()
		}
		charge += la.GetCharge().GetCents()
		paid += la.GetPaid().GetCents()
		e.Lines = append(e.Lines, rl)
	}
	e.Charge, e.Paid, e.PatientResponsibility = money(charge), money(paid), money(pr)
	return proto.Clone(e).(*remittancev1.RemittanceClaim), nil
}

// reversal repeats, with the signs flipped, the entry reported for a
// claim that has not been reversed yet. Nil when there is nothing to
// reverse: the claim was never reported, or its report was already
// reversed.
func (b *Builder) reversal(ctx context.Context, claimID string) (*remittancev1.RemittanceClaim, error) {
	doc, _ := json.Marshal(map[string]any{"claims": []map[string]string{{"claimId": claimID}}})
	sent, _, err := b.Repos.Remittances.List(ctx, store.ListOptions[*remittancev1.Remittance]{PageSize: 10_000, Where: []store.Cond{store.Contains(string(doc))}, Filter: func(r *remittancev1.Remittance) bool {
		return slices.ContainsFunc(r.GetClaims(), func(e *remittancev1.RemittanceClaim) bool { return e.GetClaimId() == claimID })
	}})
	if err != nil {
		return nil, err
	}
	reversed := map[string]bool{}
	for _, r := range sent {
		for _, e := range r.GetClaims() {
			if e.GetClaimId() == claimID && e.GetStatus() == remittancev1.RemittanceClaimStatus_REMITTANCE_CLAIM_STATUS_REVERSAL {
				reversed[e.GetReversesRemittanceId()] = true
			}
		}
	}
	for _, r := range sent {
		if reversed[r.GetId()] {
			continue
		}
		for _, e := range r.GetClaims() {
			if e.GetClaimId() != claimID || e.GetStatus() == remittancev1.RemittanceClaimStatus_REMITTANCE_CLAIM_STATUS_REVERSAL {
				continue
			}
			rev := proto.Clone(e).(*remittancev1.RemittanceClaim)
			rev.Status = remittancev1.RemittanceClaimStatus_REMITTANCE_CLAIM_STATUS_REVERSAL
			rev.ReversesRemittanceId = r.GetId()
			negate(&rev.Charge, &rev.Paid, &rev.PatientResponsibility)
			for _, l := range rev.GetLines() {
				negate(&l.Charge, &l.Paid)
				if l.Allowed != nil {
					negate(&l.Allowed)
				}
				for _, a := range l.GetAdjustments() {
					negate(&a.Amount)
				}
			}
			return rev, nil
		}
	}
	return nil, nil
}

func negate(ms ...**commonv1.Money) {
	for _, m := range ms {
		*m = money(-(*m).GetCents())
	}
}

func number(c *claimsv1.Claim) string {
	if v := c.GetClaimVersion(); v > 1 {
		return fmt.Sprintf("%s-%d", c.GetClaimNumber(), v)
	}
	if c.GetClaimNumber() == "" {
		return c.GetId()
	}
	return c.GetClaimNumber()
}

func frequencyCode(f claimsv1.ClaimFrequency) string {
	switch f {
	case claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT:
		return "7"
	case claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID:
		return "8"
	}
	return "1"
}

// facilityCode is CLP08: the place of service for a professional claim,
// the facility type (type of bill without its leading 0 and frequency
// digit) for an institutional one.
func facilityCode(c *claimsv1.Claim) string {
	if tob := c.GetInstitutional().GetTypeOfBill(); tob != "" {
		if len(tob) == 4 && tob[0] == '0' {
			tob = tob[1:]
		}
		if len(tob) >= 2 {
			return tob[:2]
		}
	}
	if len(c.GetLines()) > 0 {
		return c.GetLines()[0].GetPlaceOfServiceCode()
	}
	return ""
}

func money(cents int64) *commonv1.Money { return &commonv1.Money{Cents: cents} }
