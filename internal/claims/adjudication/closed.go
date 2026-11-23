package adjudication

import claimsv1 "coreadmin/gen/coreadmin/claims/v1"

// Codes the claims service sets, not the engine: they record what was done
// to a claim whose result had not been reported yet (its payment run not
// released), when it is changed in place instead of being replaced.
const (
	// An examiner reopened the claim. The next run doesn't raise it again.
	PendReopened = "REOPENED"
	// The provider sent a corrected claim or a void for the claim. It is
	// closed with this denial; the correction is decided as a claim of its
	// own.
	DenyReplacedByProvider = "REPLACED_BY_PROVIDER"
	DenyVoidedByProvider   = "VOIDED_BY_PROVIDER"
)

func init() {
	// No CARC chosen yet for either. Until one is, the 835 check refuses a
	// remittance carrying these lines.
	denials[DenyReplacedByProvider] = denialReport{}
	denials[DenyVoidedByProvider] = denialReport{}
}

// DenyLines denies every line of a claim for one reason.
func DenyLines(c *claimsv1.Claim, code, message string) {
	for _, l := range c.GetLines() {
		l.Adjudication = deniedLine(l, code, message)
	}
}

// ClosedByProvider reports whether a claim was closed because the provider
// replaced or voided it before its result was reported.
func ClosedByProvider(c *claimsv1.Claim) bool {
	for _, l := range c.GetLines() {
		switch l.GetAdjudication().GetDenial().GetCode() {
		case DenyReplacedByProvider, DenyVoidedByProvider:
			return true
		}
	}
	return false
}
