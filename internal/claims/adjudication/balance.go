package adjudication

import (
	"fmt"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
)

// CheckLineBalanced verifies the arithmetic invariants of a line
// adjudication. The engine runs it on every line before writing a result,
// so an unbalanced decision (and therefore an unbalanced 835) is never
// produced.
//
//	member_responsibility = sum(cost_shares)
//	allowed               = paid + member_responsibility   (payable lines)
//	paid                  = 0                              (denied lines)
//	charge - paid         = sum(cas_adjustments)
//
// Pended lines are not checked: they have no decision yet.
func CheckLineBalanced(a *claimsv1.LineAdjudication) error {
	if a.GetStatus() == claimsv1.LineStatus_LINE_STATUS_PENDED {
		return nil
	}
	charge := a.GetCharge().GetCents()
	allowed := a.GetPricing().GetAllowed().GetCents()
	paid := a.GetPaid().GetCents()
	member := a.GetMemberResponsibility().GetCents()

	var shares int64
	for _, cs := range a.GetCostShares() {
		if cs.GetAmount().GetCents() < 0 {
			return fmt.Errorf("negative cost share %s", cs.GetType())
		}
		shares += cs.GetAmount().GetCents()
	}
	if shares != member {
		return fmt.Errorf("member responsibility %d != sum of cost shares %d", member, shares)
	}

	switch a.GetStatus() {
	case claimsv1.LineStatus_LINE_STATUS_PAYABLE:
		if paid < 0 || allowed < 0 {
			return fmt.Errorf("negative amount: allowed %d, paid %d", allowed, paid)
		}
		if allowed != paid+member {
			return fmt.Errorf("allowed %d != paid %d + member responsibility %d", allowed, paid, member)
		}
	case claimsv1.LineStatus_LINE_STATUS_DENIED:
		if paid != 0 {
			return fmt.Errorf("denied line has paid %d", paid)
		}
		if a.GetDenial().GetCode() == "" {
			return fmt.Errorf("denied line has no denial code")
		}
	default:
		return fmt.Errorf("line has no status")
	}

	var cas int64
	for _, c := range a.GetCasAdjustments() {
		cas += c.GetAmount().GetCents()
	}
	if charge-paid != cas {
		return fmt.Errorf("charge %d - paid %d != sum of CAS adjustments %d", charge, paid, cas)
	}
	return nil
}
