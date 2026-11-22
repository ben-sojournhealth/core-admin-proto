package adjudication

import (
	"testing"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
)

// Every denial the engine can make is reported somehow on the 835.
func TestEveryDenialIsReported(t *testing.T) {
	for _, code := range []string{DenyNotCovered, DenyCoverageTerminated, DenyProviderSanctioned} {
		r, ok := denials[code]
		if !ok || r.CARC == "" {
			t.Errorf("%s has no CARC", code)
		}
	}
	if r := denials[DenyNotCovered]; len(r.Remarks) == 0 {
		t.Errorf("CARC 96 requires a remark code")
	}
}

// Our own denial reporting never breaks a remark rule, so
// REMARK_CODE_REQUIRED can't come from the engine's tables at runtime.
func TestDenialsSatisfyRemarkRules(t *testing.T) {
	for code, r := range denials {
		cas := []*claimsv1.CasAdjustment{{ReasonCode: r.CARC}}
		if err := CheckRemarks(cas, r.Remarks); err != nil {
			t.Errorf("%s: %v", code, err)
		}
	}
}
