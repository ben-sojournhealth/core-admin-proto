// Package ids generates internal ids: UUIDv7 (RFC 9562). The first 48 bits
// are the creation time in milliseconds, so ids sort in creation order and
// index well; the rest is random. Internal ids are never shown to people:
// claims are known by their claim number.
package ids

import (
	"crypto/rand"
	"fmt"
	"time"
)

// NewV7 returns a UUIDv7 for time t. Taking the time as an argument lets
// sandboxes with their own clock produce ids that sort in sandbox time.
func NewV7(t time.Time) string {
	var b [16]byte
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	ms := uint64(t.UnixMilli())
	for i := range 6 {
		b[i] = byte(ms >> (40 - 8*i))
	}
	b[6] = 0x70 | b[6]&0x0f // version 7
	b[8] = 0x80 | b[8]&0x3f // RFC 9562 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
