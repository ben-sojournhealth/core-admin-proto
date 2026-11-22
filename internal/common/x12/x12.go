// Package x12 reads and writes ASC X12 interchanges: the ISA/GS/ST
// envelopes and their segments. It knows nothing about any particular
// transaction (837, 835, 999); those are built on top.
package x12

import (
	"fmt"
	"strings"
)

// Delims are an interchange's separators, read from its ISA segment.
type Delims struct {
	Element, Component, Repetition, Segment byte
}

// Default is what this system writes: * : ^ ~
var Default = Delims{Element: '*', Component: ':', Repetition: '^', Segment: '~'}

// Segment is one segment. El[0] is the segment id, so El[n] is element n.
type Segment struct {
	// Position in the file, 1-based (ISA = 1).
	Pos int
	El  []string
}

func (s Segment) ID() string { return s.El[0] }

// E returns element n, or "" when absent.
func (s Segment) E(n int) string {
	if n < len(s.El) {
		return s.El[n]
	}
	return ""
}

// C returns component k (1-based) of element n.
func (s Segment) C(d Delims, n, k int) string {
	parts := strings.Split(s.E(n), string(d.Component))
	if k-1 < len(parts) {
		return parts[k-1]
	}
	return ""
}

// String renders the segment with d's separators, without the terminator.
func (s Segment) String(d Delims) string { return strings.Join(s.El, string(d.Element)) }

// Interchange is a parsed file. Envelope problems inside a group or set
// are not errors here: they are recorded on the Set for the acknowledgment.
type Interchange struct {
	Delims   Delims
	ISA, IEA Segment
	Groups   []*Group
	// Every segment in file order.
	Segments []Segment
}

type Group struct {
	GS, GE Segment
	Sets   []*Set
}

// Set is one transaction set, ST to SE.
type Set struct {
	ST, SE Segment
	// Segments between ST and SE.
	Body []Segment
	// Structural problems with the envelope (SE count, control numbers).
	Problems []string
}

// Sender is ISA06, trimmed.
func (ic *Interchange) Sender() string { return strings.TrimSpace(ic.ISA.E(6)) }

// Receiver is ISA08, trimmed.
func (ic *Interchange) Receiver() string { return strings.TrimSpace(ic.ISA.E(8)) }

// ControlNumber is ISA13.
func (ic *Interchange) ControlNumber() string { return ic.ISA.E(13) }

// Parse reads an interchange. It fails only when the file cannot be read
// as X12 at all (no usable ISA, or segments outside any envelope); that
// is answered with a TA1 rather than a 999.
func Parse(raw []byte) (*Interchange, error) {
	text := strings.TrimLeft(string(raw), " \t\r\n\ufeff")
	if !strings.HasPrefix(text, "ISA") || len(text) < 106 {
		return nil, fmt.Errorf("not an X12 interchange: it does not start with an ISA segment")
	}
	d := Delims{Element: text[3]}
	// ISA has 16 elements; ISA16 is the one-character component separator
	// and the next character terminates the segment.
	n, i := 0, 0
	for ; i < len(text) && n < 16; i++ {
		if text[i] == d.Element {
			n++
		}
	}
	if n < 16 || i+1 >= len(text) {
		return nil, fmt.Errorf("ISA segment is incomplete")
	}
	d.Component, d.Segment = text[i], text[i+1]
	ic := &Interchange{Delims: d}

	pos := 0
	for _, s := range strings.Split(text, string(d.Segment)) {
		s = strings.Trim(s, "\r\n\t ")
		if s == "" {
			continue
		}
		pos++
		ic.Segments = append(ic.Segments, Segment{Pos: pos, El: strings.Split(s, string(d.Element))})
	}
	ic.ISA = ic.Segments[0]
	if r := ic.ISA.E(11); len(r) == 1 && r != "U" {
		ic.Delims.Repetition = r[0]
	}

	var g *Group
	var set *Set
	for _, s := range ic.Segments[1:] {
		switch s.ID() {
		case "GS":
			g = &Group{GS: s}
			ic.Groups = append(ic.Groups, g)
		case "GE":
			if g == nil {
				return nil, fmt.Errorf("segment %d: GE without GS", s.Pos)
			}
			g.GE, g = s, nil
		case "ST":
			if g == nil {
				return nil, fmt.Errorf("segment %d: ST outside a functional group", s.Pos)
			}
			set = &Set{ST: s}
			g.Sets = append(g.Sets, set)
		case "SE":
			if set == nil {
				return nil, fmt.Errorf("segment %d: SE without ST", s.Pos)
			}
			set.SE = s
			if want := fmt.Sprint(len(set.Body) + 2); s.E(1) != want {
				set.Problems = append(set.Problems, fmt.Sprintf("SE01 says %s segments, the set has %s", s.E(1), want))
			}
			if s.E(2) != set.ST.E(2) {
				set.Problems = append(set.Problems, fmt.Sprintf("SE02 %s does not match ST02 %s", s.E(2), set.ST.E(2)))
			}
			set = nil
		case "IEA":
			ic.IEA = s
		default:
			if set == nil {
				return nil, fmt.Errorf("segment %d (%s) is outside a transaction set", s.Pos, s.ID())
			}
			set.Body = append(set.Body, s)
		}
	}
	if set != nil {
		set.Problems = append(set.Problems, "transaction set has no SE trailer")
	}
	return ic, nil
}

// Writer builds an interchange with the Default separators, one segment
// per line.
type Writer struct {
	b strings.Builder
	// Segments written since the last ST, including it.
	inSet int
}

// Seg writes one segment. Trailing empty elements are dropped.
func (w *Writer) Seg(id string, el ...string) {
	for len(el) > 0 && el[len(el)-1] == "" {
		el = el[:len(el)-1]
	}
	w.b.WriteString(strings.Join(append([]string{id}, el...), "*"))
	w.b.WriteString("~\n")
	if id == "ST" {
		w.inSet = 0
	}
	w.inSet++
}

// SE writes the set trailer with the right segment count.
func (w *Writer) SE(control string) { w.Seg("SE", fmt.Sprint(w.inSet+1), control) }

// ISA writes a fixed-width ISA segment.
func (w *Writer) ISA(sender, receiver, date, time, control, usage string) {
	w.Seg("ISA", "00", pad("", 10), "00", pad("", 10), "ZZ", pad(sender, 15), "ZZ", pad(receiver, 15),
		date, time, "^", "00501", control, "0", usage, ":")
}

func (w *Writer) String() string { return w.b.String() }

func pad(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}
