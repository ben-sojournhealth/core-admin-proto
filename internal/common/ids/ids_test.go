package ids_test

import (
	"regexp"
	"testing"
	"time"

	"coreadmin/internal/common/ids"
)

var v7 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewV7(t *testing.T) {
	t0 := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	a, b := ids.NewV7(t0), ids.NewV7(t0.Add(time.Millisecond))
	for _, id := range []string{a, b} {
		if !v7.MatchString(id) {
			t.Errorf("%q is not a UUIDv7", id)
		}
	}
	if a >= b {
		t.Errorf("ids do not sort by time: %s >= %s", a, b)
	}
	if ids.NewV7(t0) == a {
		t.Error("two ids for the same millisecond are equal")
	}
}
