// Package claimsintake loads inbound claim files: X12 837P from the
// clearinghouse. A file is checked for structure (answered with a 999),
// each accepted transaction set is mapped to Claims, and every claim is
// handed to claims.Service.Receive.
//
// Intake runs once per file and only reads: it copies the submitted fields
// onto the Claim as sent and looks nothing up. Matching the member,
// provider and original claim is adjudication's job, so it can run again
// when that data changes. The 999 rejects only what cannot be read
// reliably (envelope errors, missing required segments and elements, bad
// codes).
package claimsintake

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	"coreadmin/internal/x12"
	"coreadmin/internal/x12ack"
)

// Guide837P is the implementation guide this package reads.
const Guide837P = "005010X222A1"

// ParsedClaim is one CLM loop mapped to a Claim.
type ParsedClaim struct {
	Claim *claimsv1.Claim
	// Segment positions in the file of the CLM and the claim's last
	// segment.
	First, Last int
}

// subscriber is the 2000B loop, with the 2000C patient if one was sent.
type subscriber struct {
	card        string
	name        *commonv1.PersonName
	born        *commonv1.Date
	gender      string
	patient     *commonv1.PersonName
	patientBorn *commonv1.Date
	patientSex  string
	dependent   bool
}

// Map837P maps one transaction set. It returns the claims and the errors
// that make the set unreadable; with any errors the set is rejected and
// the claims are discarded.
func Map837P(d x12.Delims, set *x12.Set) ([]ParsedClaim, []x12ack.Error) {
	var (
		out  []ParsedClaim
		errs []x12ack.Error
		cur  *ParsedClaim
		line *claimsv1.ClaimLine
		// Loop state.
		level            string // HL03 of the current HL: 20, 22 or 23
		billing          *claimsv1.ProviderRef
		sub              subscriber
		chReceived       *timestamppb.Timestamp
		lastNM1          string
		claimPOS         string
		claimDOS         *commonv1.DateRange
		lastClaimSegment int
	)
	billing = &claimsv1.ProviderRef{}
	bad := func(s x12.Segment, el int, code, format string, args ...any) {
		errs = append(errs, x12ack.Bad(s, el, code, format, args...))
	}
	missing := func(s x12.Segment, id, format string, args ...any) {
		errs = append(errs, x12ack.Missing(s.Pos, id, format, args...))
	}
	finish := func() {
		if cur == nil {
			return
		}
		c := cur.Claim
		cur.Last = lastClaimSegment
		if len(c.GetLines()) == 0 {
			missing(x12.Segment{Pos: cur.First}, "LX", "claim %s has no service lines", c.GetPatientControlNumber())
		}
		var start, end *commonv1.Date
		for _, l := range c.GetLines() {
			if l.GetServiceDates() == nil {
				if claimDOS == nil {
					missing(x12.Segment{Pos: cur.First}, "DTP", "claim %s line %d has no service date", c.GetPatientControlNumber(), l.GetLineNumber())
					continue
				}
				l.ServiceDates = claimDOS
			}
			if l.GetPlaceOfServiceCode() == "" {
				l.PlaceOfServiceCode = claimPOS
			}
			if start == nil || before(l.GetServiceDates().GetStart(), start) {
				start = l.GetServiceDates().GetStart()
			}
			e := l.GetServiceDates().GetEnd()
			if e == nil {
				e = l.GetServiceDates().GetStart()
			}
			if end == nil || before(end, e) {
				end = e
			}
		}
		if start != nil {
			c.ServiceDates = &commonv1.DateRange{Start: start, End: end}
		}
		out = append(out, *cur)
		cur, line, claimDOS, claimPOS = nil, nil, nil, ""
	}

	for _, s := range set.Body {
		if cur != nil && s.ID() != "HL" {
			lastClaimSegment = s.Pos
		}
		switch s.ID() {
		case "BHT":
			// BHT04/05: when the clearinghouse created this transaction,
			// which is when it accepted the claims in it.
			if t, err := time.Parse("20060102 1504", s.E(4)+" "+padTime(s.E(5))); err == nil {
				chReceived = timestamppb.New(t)
			} else {
				bad(s, 4, "8", "BHT04 %q is not a date", s.E(4))
			}

		case "HL":
			finish()
			level = s.E(3)
			switch level {
			case "20":
				billing = &claimsv1.ProviderRef{}
			case "22":
				sub = subscriber{}
			case "23":
				sub.dependent = true
				sub.patient, sub.patientBorn, sub.patientSex = nil, nil, ""
			default:
				bad(s, 3, "7", "HL03 %q is not a hierarchical level of the 837P", s.E(3))
			}

		case "NM1":
			lastNM1 = s.E(1)
			name := &commonv1.PersonName{Last: s.E(3), First: s.E(4), Middle: s.E(5)}
			switch s.E(1) {
			case "85": // 2010AA billing provider
				billing.Name = strings.TrimSpace(strings.Join([]string{s.E(4), s.E(3)}, " "))
				billing.Npi = s.E(9)
				if s.E(8) != "XX" || len(s.E(9)) != 10 {
					bad(s, 9, "7", "billing provider NPI %q is not a 10-digit NPI", s.E(9))
				}
			case "IL": // 2010BA subscriber
				sub.card, sub.name = s.E(9), name
				if s.E(9) == "" {
					bad(s, 9, "1", "subscriber has no member id")
				}
			case "QC": // 2010CA patient
				sub.patient = name
			case "82": // 2310B rendering provider (claim level only)
				if cur != nil && line == nil {
					cur.Claim.RenderingProvider = &claimsv1.ProviderRef{Npi: s.E(9), Name: strings.TrimSpace(s.E(4) + " " + s.E(3))}
				}
			case "DN": // 2310A referring provider
				if cur != nil && line == nil {
					cur.Claim.ReferringProvider = &claimsv1.ProviderRef{Npi: s.E(9), Name: strings.TrimSpace(s.E(4) + " " + s.E(3))}
				}
			}

		case "DMG":
			born, ok := parseDate(s.E(2))
			if !ok {
				bad(s, 2, "8", "DMG02 %q is not a date", s.E(2))
			}
			switch lastNM1 {
			case "IL":
				sub.born, sub.gender = born, s.E(3)
			case "QC":
				sub.patientBorn, sub.patientSex = born, s.E(3)
			}

		case "REF":
			switch {
			case level == "20" && cur == nil && s.E(1) == "EI":
				billing.Tin = s.E(2)
			case cur != nil && line == nil && s.E(1) == "F8":
				cur.Claim.PayerClaimControlNumber = s.E(2)
			case cur != nil && line == nil && s.E(1) == "D9":
				cur.Claim.ClearinghouseTraceNumber = s.E(2)
			case cur != nil && line == nil && (s.E(1) == "G1" || s.E(1) == "9F"):
				cur.Claim.AuthorizationNumbers = append(cur.Claim.AuthorizationNumbers, s.E(2))
			}

		case "PRV":
			if cur != nil && line == nil && lastNM1 == "82" && cur.Claim.GetRenderingProvider() != nil {
				cur.Claim.RenderingProvider.TaxonomyCode = s.E(3)
			}

		case "CLM":
			finish()
			if level != "22" && level != "23" {
				bad(s, 0, "", "CLM is not under a subscriber or patient")
				continue
			}
			cur = &ParsedClaim{First: s.Pos}
			lastClaimSegment = s.Pos
			b := proto.Clone(billing).(*claimsv1.ProviderRef)
			c := &claimsv1.Claim{
				Type:                      claimsv1.ClaimType_CLAIM_TYPE_PROFESSIONAL,
				Source:                    claimsv1.ClaimSource_CLAIM_SOURCE_EDI_837,
				PatientControlNumber:      s.E(1),
				MemberCardId:              sub.card,
				BillingProvider:           b,
				ClearinghouseReceivedTime: chReceived,
				SubmittedPatient: &claimsv1.SubmittedPatient{
					CardId: sub.card, Name: sub.name, BirthDate: sub.born, GenderCode: sub.gender,
				},
			}
			if sub.dependent {
				c.SubmittedPatient.Name, c.SubmittedPatient.BirthDate, c.SubmittedPatient.GenderCode = sub.patient, sub.patientBorn, sub.patientSex
				c.SubmittedPatient.DependentLoop = true
			}
			cur.Claim = c
			if s.E(1) == "" {
				bad(s, 1, "1", "CLM01 patient control number is missing")
			}
			if total, ok := parseCents(s.E(2)); ok {
				c.TotalCharge = &commonv1.Money{Cents: total}
			} else {
				bad(s, 2, "1", "CLM02 total charge %q is missing or not an amount", s.E(2))
			}
			claimPOS = s.C(d, 5, 1)
			switch s.C(d, 5, 3) {
			case "1":
				c.Frequency = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_ORIGINAL
			case "7":
				c.Frequency = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT
			case "8":
				c.Frequency = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID
			default:
				bad(s, 5, "7", "CLM05-3 claim frequency %q is not 1, 7 or 8", s.C(d, 5, 3))
			}

		case "HI":
			if cur == nil {
				continue
			}
			for i := 1; i < len(s.El); i++ {
				if code := s.C(d, i, 2); code != "" {
					cur.Claim.DiagnosisCodes = append(cur.Claim.DiagnosisCodes, code)
				}
			}

		case "DTP":
			if cur == nil || s.E(1) != "472" {
				continue
			}
			r, ok := parseRange(s.E(2), s.E(3))
			if !ok {
				bad(s, 3, "8", "DTP03 %q is not a date", s.E(3))
				continue
			}
			if line != nil {
				line.ServiceDates = r
			} else {
				claimDOS = r
			}

		case "LX":
			if cur == nil {
				bad(s, 0, "", "LX outside a claim")
				continue
			}
			line = &claimsv1.ClaimLine{}
			n, err := strconv.Atoi(s.E(1))
			if err != nil {
				bad(s, 1, "6", "LX01 %q is not a line number", s.E(1))
			}
			line.LineNumber = int32(n)
			cur.Claim.Lines = append(cur.Claim.Lines, line)

		case "SV1":
			if line == nil {
				bad(s, 0, "", "SV1 without LX")
				continue
			}
			if s.C(d, 1, 1) != "HC" || s.C(d, 1, 2) == "" {
				bad(s, 1, "1", "SV101 has no HCPCS/CPT procedure code")
			}
			line.ProcedureCode = s.C(d, 1, 2)
			for k := 3; k <= 6; k++ {
				if m := s.C(d, 1, k); m != "" {
					line.Modifiers = append(line.Modifiers, m)
				}
			}
			if charge, ok := parseCents(s.E(2)); ok {
				line.Charge = &commonv1.Money{Cents: charge}
			} else {
				bad(s, 2, "1", "SV102 line charge %q is missing or not an amount", s.E(2))
			}
			if units, err := strconv.ParseFloat(s.E(4), 64); err == nil {
				line.UnitsMilli = int64(units*1000 + 0.5)
			} else {
				bad(s, 4, "6", "SV104 units %q is not a number", s.E(4))
			}
			line.PlaceOfServiceCode = s.E(5)
			for k := 1; k <= 4; k++ {
				if p, err := strconv.Atoi(s.C(d, 7, k)); err == nil {
					line.DiagnosisPointers = append(line.DiagnosisPointers, int32(p))
				}
			}
		}
	}
	finish()
	if chReceived == nil {
		missing(set.ST, "BHT", "transaction set has no BHT")
	}
	return out, errs
}

func before(a, b *commonv1.Date) bool {
	return fmt.Sprintf("%04d%02d%02d", a.GetYear(), a.GetMonth(), a.GetDay()) < fmt.Sprintf("%04d%02d%02d", b.GetYear(), b.GetMonth(), b.GetDay())
}

func padTime(t string) string {
	if len(t) >= 4 {
		return t[:4]
	}
	return "0000"
}

func parseDate(s string) (*commonv1.Date, bool) {
	t, err := time.Parse("20060102", s)
	if err != nil {
		return nil, false
	}
	return &commonv1.Date{Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day())}, true
}

// parseRange reads a DTP date: D8 (one date) or RD8 (from-to).
func parseRange(format, v string) (*commonv1.DateRange, bool) {
	switch format {
	case "D8":
		d, ok := parseDate(v)
		return &commonv1.DateRange{Start: d, End: d}, ok
	case "RD8":
		from, to, found := strings.Cut(v, "-")
		a, ok1 := parseDate(from)
		b, ok2 := parseDate(to)
		return &commonv1.DateRange{Start: a, End: b}, found && ok1 && ok2
	}
	return nil, false
}

// parseCents reads an X12 decimal amount ("150", "150.5", "150.25").
func parseCents(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	whole, frac, _ := strings.Cut(s, ".")
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || len(frac) > 2 {
		return 0, false
	}
	f := int64(0)
	if frac != "" {
		if f, err = strconv.ParseInt((frac + "0")[:2], 10, 64); err != nil {
			return 0, false
		}
	}
	return w*100 + f, true
}
