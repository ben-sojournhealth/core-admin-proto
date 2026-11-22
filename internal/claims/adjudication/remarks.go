package adjudication

import (
	"fmt"
	"slices"
	"strings"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
)

// remarkRules lists the CARCs that must be accompanied, on the same line,
// by at least one of these remark codes (RARCs). A line that breaks a
// rule pends with PendRemarkCodeRequired rather than reaching an 835.
//
// The CARC's own usage note says which need a remark code; the allowed
// RARCs are our choice of what may explain it.
var remarkRules = map[string][]string{
	// 96: non-covered charge(s). At least one remark code is required.
	// N130: consult plan benefit documents for restrictions on this
	// service.
	"96": {"N130"},
}

// CheckRemarks reports the first CARC on a line that lacks one of its
// required remark codes.
func CheckRemarks(cas []*claimsv1.CasAdjustment, remarks []string) error {
	for _, a := range cas {
		allowed, ok := remarkRules[a.GetReasonCode()]
		if !ok {
			continue
		}
		if !slices.ContainsFunc(remarks, func(r string) bool { return slices.Contains(allowed, r) }) {
			got := "none"
			if len(remarks) > 0 {
				got = strings.Join(remarks, ", ")
			}
			return fmt.Errorf("CARC %s needs one of the remark codes %s; the line has %s", a.GetReasonCode(), strings.Join(allowed, ", "), got)
		}
	}
	return nil
}

// checkRemarks pends every decided line whose CARCs lack a required
// remark code. The line keeps what was calculated, so an examiner can see
// what it would have reported; it pends until a later run gets the codes
// right (the rules or the denial's reporting changed).
func checkRemarks(c *claimsv1.Claim) {
	adj := c.GetAdjudication()
	for _, l := range c.GetLines() {
		la := l.GetAdjudication()
		if la.GetStatus() != claimsv1.LineStatus_LINE_STATUS_PAYABLE && la.GetStatus() != claimsv1.LineStatus_LINE_STATUS_DENIED {
			continue
		}
		if err := CheckRemarks(la.GetCasAdjustments(), la.GetRemarkCodes()); err != nil {
			la.Status = claimsv1.LineStatus_LINE_STATUS_PENDED
			adj.Pends = append(adj.Pends, &claimsv1.Pend{Code: PendRemarkCodeRequired, Message: err.Error(), LineNumber: l.GetLineNumber()})
		}
	}
}
