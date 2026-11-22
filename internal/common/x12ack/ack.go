// Package x12ack builds 999 implementation acknowledgments
// (005010X231A1): for each transaction set in an inbound interchange,
// accepted or rejected and why. Every X12 intake answers with one.
package x12ack

import (
	"fmt"
	"strings"
	"time"

	"coreadmin/internal/common/x12"
)

// Guide is the 999's implementation guide.
const Guide = "005010X231A1"

// Error is one problem in a transaction set: an IK3 (segment) error,
// optionally narrowed to an IK4 (element) error. With no SegmentCode it is
// an envelope problem (SE count or control number), reported only as IK5
// code 4.
type Error struct {
	Segment x12.Segment
	// IK3 segment syntax error code, e.g. "8" segment has data element
	// errors, "3" required segment missing.
	SegmentCode string
	// Element position and IK4 code, when the error is in one element.
	Element     int
	ElementCode string
	Message     string
}

// Code is a short label for the error: IK4-<code>, IK3-<code> or IK5.
func (e Error) Code() string {
	switch {
	case e.ElementCode != "":
		return fmt.Sprintf("IK4-%s", e.ElementCode)
	case e.SegmentCode != "":
		return fmt.Sprintf("IK3-%s", e.SegmentCode)
	}
	return "IK5"
}

// Bad is an error in one element of a segment that is present (IK3 code
// 8).
func Bad(s x12.Segment, el int, code, format string, args ...any) Error {
	return Error{Segment: s, SegmentCode: "8", Element: el, ElementCode: code, Message: fmt.Sprintf(format, args...)}
}

// Missing is a required segment that is absent (IK3 code 3), reported at
// position pos.
func Missing(pos int, id, format string, args ...any) Error {
	return Error{Segment: x12.Segment{Pos: pos, El: []string{id}}, SegmentCode: "3", Message: fmt.Sprintf(format, args...)}
}

// Builder accumulates the 999 for one interchange.
type Builder struct {
	IC *x12.Interchange
	// Our id: ISA06 of the 999.
	Sender string
	Now    time.Time

	sets     []string // rendered AK2..IK5 blocks
	accepted int
	received int
}

// Add reports one transaction set: accepted when errs is empty.
func (a *Builder) Add(set *x12.Set, errs []Error) {
	a.received++
	var w x12.Writer
	w.Seg("AK2", set.ST.E(1), set.ST.E(2), set.ST.E(3))
	envelope := false
	for _, e := range errs {
		if e.SegmentCode == "" {
			envelope = true
			continue
		}
		// IK302 is the segment's position in the set (ST = 1).
		w.Seg("IK3", e.Segment.ID(), fmt.Sprint(e.Segment.Pos-set.ST.Pos+1), "", e.SegmentCode)
		if e.Element > 0 {
			w.Seg("IK4", fmt.Sprint(e.Element), "", e.ElementCode, truncate(e.Segment.E(e.Element), 99))
		}
	}
	switch {
	case len(errs) == 0:
		a.accepted++
		w.Seg("IK5", "A")
	case envelope:
		w.Seg("IK5", "R", "4")
	default:
		w.Seg("IK5", "R", "5")
	}
	a.sets = append(a.sets, w.String())
}

// String renders the 999 interchange.
func (a *Builder) String() string {
	var w x12.Writer
	gs := x12.Segment{El: []string{"GS"}}
	if len(a.IC.Groups) > 0 {
		gs = a.IC.Groups[0].GS
	}
	date, tm := a.Now.Format("060102"), a.Now.Format("1504")
	control := fmt.Sprintf("%09d", a.Now.Unix()%1_000_000_000)
	w.ISA(a.Sender, a.IC.Sender(), date, tm, control, a.IC.ISA.E(15))
	w.Seg("GS", "FA", a.Sender, a.IC.Sender(), a.Now.Format("20060102"), tm, gs.E(6), "X", Guide)
	w.Seg("ST", "999", "0001", Guide)
	w.Seg("AK1", gs.E(1), gs.E(6), gs.E(8))
	out := w.String()
	for _, s := range a.sets {
		out += s
	}
	status := "A"
	switch {
	case a.accepted == 0:
		status = "R"
	case a.accepted < a.received:
		status = "P"
	}
	var t x12.Writer
	t.Seg("AK9", status, fmt.Sprint(a.received), fmt.Sprint(a.received), fmt.Sprint(a.accepted))
	segs := strings.Count(out, "~") - 2 + 1 // segments since ST, plus AK9
	t.Seg("SE", fmt.Sprint(segs+1), "0001")
	t.Seg("GE", "1", gs.E(6))
	t.Seg("IEA", "1", control)
	return out + t.String()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
