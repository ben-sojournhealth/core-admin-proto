package adjudication

import "slices"

// SetDenialRemarks changes the remark codes a denial reports, for a test
// that needs a line to break a remark rule. It returns the undo.
func SetDenialRemarks(code string, remarks []string) func() {
	r := denials[code]
	old := slices.Clone(r.Remarks)
	r.Remarks = remarks
	denials[code] = r
	return func() { r.Remarks = old; denials[code] = r }
}
