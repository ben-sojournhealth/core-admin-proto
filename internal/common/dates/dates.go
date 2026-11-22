// Package dates has helpers for common.v1.Date and DateRange.
package dates

import (
	"fmt"
	"time"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
)

// Cmp returns -1, 0 or 1 as a is before, equal to or after b.
func Cmp(a, b *commonv1.Date) int {
	av := [3]int32{a.GetYear(), a.GetMonth(), a.GetDay()}
	bv := [3]int32{b.GetYear(), b.GetMonth(), b.GetDay()}
	for i := range av {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// In reports whether d falls in r. Ranges are inclusive; an unset start or
// end is open.
func In(d *commonv1.Date, r *commonv1.DateRange) bool {
	if r.GetStart() != nil && Cmp(d, r.GetStart()) < 0 {
		return false
	}
	if r.GetEnd() != nil && Cmp(d, r.GetEnd()) > 0 {
		return false
	}
	return true
}

// Format renders YYYY-MM-DD ("" for nil).
func Format(d *commonv1.Date) string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
}

// Parse reads YYYY-MM-DD.
func Parse(s string) (*commonv1.Date, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, fmt.Errorf("dates: %q is not YYYY-MM-DD", s)
	}
	return FromTime(t), nil
}

func FromTime(t time.Time) *commonv1.Date {
	return &commonv1.Date{Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day())}
}
