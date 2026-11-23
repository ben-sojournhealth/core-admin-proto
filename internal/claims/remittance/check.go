package remittance

import (
	"fmt"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	remittancev1 "coreadmin/gen/coreadmin/remittance/v1"
	"coreadmin/internal/claims/adjudication"
)

// Check verifies the 835 balancing rules, so an unbalanced remittance is
// never built:
//
//	line:     charge - sum(CAS)               = paid
//	claim:    charge                          = sum(line charges)
//	          paid                            = sum(line paid)
//	          patient responsibility          = sum(PR adjustments)
//	payment:  amount                          = sum(claim paid)
//
// the payment method fits the amount (NONE exactly when nothing is paid),
// and every CARC that needs a remark code has one of its allowed ones
// (adjudication.CheckRemarks; adjudication pends a line that doesn't).
func Check(r *remittancev1.Remittance) error {
	var total int64
	for _, e := range r.GetClaims() {
		var charge, paid, pr int64
		for _, l := range e.GetLines() {
			var cas int64
			if err := adjudication.CheckRemarks(l.GetAdjustments(), l.GetRemarkCodes()); err != nil {
				return fmt.Errorf("remittance: %s line %d: %w", e.GetClaimNumber(), l.GetLineNumber(), err)
			}
			for _, a := range l.GetAdjustments() {
				if a.GetReasonCode() == "" {
					return fmt.Errorf("remittance: %s line %d: an adjustment has no reason code (CARC): none has been chosen for this denial", e.GetClaimNumber(), l.GetLineNumber())
				}
				cas += a.GetAmount().GetCents()
				if a.GetGroup() == claimsv1.CasGroup_CAS_GROUP_PATIENT_RESPONSIBILITY {
					pr += a.GetAmount().GetCents()
				}
			}
			if l.GetCharge().GetCents()-cas != l.GetPaid().GetCents() {
				return fmt.Errorf("remittance: %s line %d: charge %d - adjustments %d != paid %d",
					e.GetClaimNumber(), l.GetLineNumber(), l.GetCharge().GetCents(), cas, l.GetPaid().GetCents())
			}
			charge += l.GetCharge().GetCents()
			paid += l.GetPaid().GetCents()
		}
		switch {
		case e.GetCharge().GetCents() != charge:
			return fmt.Errorf("remittance: %s: charge %d != sum of lines %d", e.GetClaimNumber(), e.GetCharge().GetCents(), charge)
		case e.GetPaid().GetCents() != paid:
			return fmt.Errorf("remittance: %s: paid %d != sum of lines %d", e.GetClaimNumber(), e.GetPaid().GetCents(), paid)
		case e.GetPatientResponsibility().GetCents() != pr:
			return fmt.Errorf("remittance: %s: patient responsibility %d != PR adjustments %d", e.GetClaimNumber(), e.GetPatientResponsibility().GetCents(), pr)
		}
		total += paid
	}
	p := r.GetPayment()
	if p.GetAmount().GetCents() != total {
		return fmt.Errorf("remittance: payment %d != sum of claims paid %d", p.GetAmount().GetCents(), total)
	}
	if none := p.GetMethod() == remittancev1.PaymentMethod_PAYMENT_METHOD_NONE; none != (total == 0) {
		return fmt.Errorf("remittance: payment of %d cents by %s", total, p.GetMethod())
	}
	return nil
}
