// Package enrollmentintake loads inbound enrollment files: X12 834s from
// plan sponsors (employers) and, for some clients, COBRA administrators.
// A file is checked for structure (answered with a 999), and each member
// loop of an accepted transaction set is captured as an
// EnrollmentTransaction and handed on to eligibility.
//
// Intake runs once per file and only reads: it copies each member loop as
// sent and looks nothing up. Which customer, member and plan a loop means,
// and what it changes, is decided when eligibility applies it. The 999
// rejects only what cannot be read reliably (envelope errors, missing
// required segments and elements, bad codes and dates).
package enrollmentintake

import (
	"fmt"
	"slices"
	"strings"
	"time"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	"coreadmin/internal/x12"
	"coreadmin/internal/x12ack"
)

// Guide834 is the implementation guide this package reads.
const Guide834 = "005010X220A1"

// ParsedMember is one member loop (INS) mapped to an EnrollmentTransaction.
type ParsedMember struct {
	Txn *eligibilityv1.EnrollmentTransaction
	// Segment positions in the file of the INS and the loop's last
	// segment.
	First, Last int
}

// The small, fixed code sets mapped to enums. A code that isn't here maps
// to UNKNOWN and is kept as sent; the 999 doesn't reject it.
var (
	purposes = map[string]eligibilityv1.EnrollmentPurpose{
		"00": eligibilityv1.EnrollmentPurpose_ENROLLMENT_PURPOSE_ORIGINAL,
		"15": eligibilityv1.EnrollmentPurpose_ENROLLMENT_PURPOSE_RESUBMISSION,
		"22": eligibilityv1.EnrollmentPurpose_ENROLLMENT_PURPOSE_INFORMATION_COPY,
	}
	actionCodes = map[string]eligibilityv1.EnrollmentAction{
		"2":  eligibilityv1.EnrollmentAction_ENROLLMENT_ACTION_CHANGE,
		"4":  eligibilityv1.EnrollmentAction_ENROLLMENT_ACTION_VERIFY,
		"RX": eligibilityv1.EnrollmentAction_ENROLLMENT_ACTION_REPLACE,
	}
	maintenanceTypes = map[string]eligibilityv1.MaintenanceType{
		"001": eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_CHANGE,
		"021": eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_ADDITION,
		"024": eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_TERMINATION,
		"025": eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_REINSTATEMENT,
		"030": eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_AUDIT,
	}
	benefitStatuses = map[string]eligibilityv1.BenefitStatus{
		"A": eligibilityv1.BenefitStatus_BENEFIT_STATUS_ACTIVE,
		"C": eligibilityv1.BenefitStatus_BENEFIT_STATUS_COBRA,
		"S": eligibilityv1.BenefitStatus_BENEFIT_STATUS_SURVIVING_INSURED,
		"T": eligibilityv1.BenefitStatus_BENEFIT_STATUS_TEFRA,
	}
)

// lookup maps a code through table: the zero value when it wasn't sent,
// unknown (and the code kept in *unrecognized) when it isn't in the table.
func lookup[E ~int32](s x12.Segment, el int, table map[string]E, unknown E, unrecognized *[]*eligibilityv1.UnrecognizedCode) E {
	v := s.E(el)
	if v == "" {
		return 0
	}
	if e, ok := table[v]; ok {
		return e
	}
	*unrecognized = append(*unrecognized, &eligibilityv1.UnrecognizedCode{Element: fmt.Sprintf("%s%02d", s.ID(), el), Value: v})
	return unknown
}

// Map834 maps one transaction set. It returns the member loops and the
// errors that make the set unreadable; with any errors the set is
// rejected and its members are discarded.
func Map834(set *x12.Set) ([]ParsedMember, []x12ack.Error) {
	var (
		out    []ParsedMember
		errs   []x12ack.Error
		cur    *ParsedMember
		hd     *eligibilityv1.HealthCoverage
		header = &eligibilityv1.EnrollmentHeader{}
		// Entity of the last NM1 in the member loop: N3, N4 and PER
		// belong to it.
		nm1          string
		sawBGN       bool
		hasSub, hasN = map[*ParsedMember]bool{}, map[*ParsedMember]bool{}
	)
	bad := func(s x12.Segment, el int, code, format string, args ...any) {
		errs = append(errs, x12ack.Bad(s, el, code, format, args...))
	}
	required := func(s x12.Segment, els ...int) bool {
		ok := true
		for _, el := range els {
			if s.E(el) == "" {
				bad(s, el, "1", "%s%02d is required", s.ID(), el)
				ok = false
			}
		}
		return ok
	}
	code := func(s x12.Segment, el int, allowed []string, what string) {
		if v := s.E(el); v != "" && !slices.Contains(allowed, v) {
			bad(s, el, "7", "%s %q is not a valid code (%s)", what, v, strings.Join(allowed, ", "))
		}
	}
	const unknownMaint = eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_UNKNOWN
	dtp := func(s x12.Segment) *eligibilityv1.QualifiedDate {
		if !required(s, 1, 2, 3) {
			return nil
		}
		q := &eligibilityv1.QualifiedDate{Qualifier: s.E(1)}
		var ok bool
		switch s.E(2) {
		case "D8":
			q.Date, ok = parseDate(s.E(3))
		case "RD8":
			from, to, _ := strings.Cut(s.E(3), "-")
			var ok2 bool
			q.Date, ok = parseDate(from)
			q.End, ok2 = parseDate(to)
			ok = ok && ok2
		default:
			bad(s, 2, "7", "date format %q is not D8 or RD8", s.E(2))
			return nil
		}
		if !ok {
			bad(s, 3, "8", "%q is not a valid date", s.E(3))
			return nil
		}
		return q
	}
	ref := func(s x12.Segment) *eligibilityv1.QualifiedRef {
		if !required(s, 1, 2) {
			return nil
		}
		return &eligibilityv1.QualifiedRef{Qualifier: s.E(1), Value: s.E(2)}
	}
	finish := func(last int) {
		if cur == nil {
			return
		}
		cur.Last = last
		if !hasSub[cur] {
			errs = append(errs, x12ack.Missing(cur.First, "REF", "member loop at segment %d has no REF*0F subscriber number", cur.First))
		}
		if !hasN[cur] {
			errs = append(errs, x12ack.Missing(cur.First, "NM1", "member loop at segment %d has no NM1*IL member name", cur.First))
		}
		out = append(out, *cur)
		cur, hd, nm1 = nil, nil, ""
	}
	unmapped := func(s x12.Segment) string { return s.String(x12.Default) + "~" }

	last := set.ST.Pos
	for _, s := range set.Body {
		switch {
		case s.ID() == "INS":
			finish(last)
			t := &eligibilityv1.EnrollmentTransaction{Header: header}
			cur = &ParsedMember{Txn: t, First: s.Pos}
			required(s, 1, 2, 3, 5)
			// A subscriber indicator has no unknown: it is Y or N.
			code(s, 1, []string{"Y", "N"}, "subscriber indicator")
			t.Subscriber = s.E(1) == "Y"
			t.RelationshipCode, t.MaintenanceReasonCode = s.E(2), s.E(4)
			t.MaintenanceType = lookup(s, 3, maintenanceTypes, unknownMaint, &t.UnrecognizedCodes)
			t.BenefitStatus = lookup(s, 5, benefitStatuses, eligibilityv1.BenefitStatus_BENEFIT_STATUS_UNKNOWN, &t.UnrecognizedCodes)
			t.MedicarePlanCode = strings.SplitN(s.E(6), string(x12.Default.Component), 2)[0]
			t.CobraQualifyingEventCode, t.EmploymentStatusCode = s.E(7), s.E(8)
			t.StudentStatusCode, t.HandicapIndicator = s.E(9), s.E(10)
			if s.E(12) != "" {
				d, ok := parseDate(s.E(12))
				if !ok {
					bad(s, 12, "8", "date of death %q is not a valid date", s.E(12))
				}
				t.DeathDate = d
			}

		case cur == nil:
			// Header: before the first member loop.
			switch s.ID() {
			case "BGN":
				sawBGN = true
				required(s, 1, 2, 3, 8)
				header.Reference = s.E(2)
				header.Purpose = lookup(s, 1, purposes, eligibilityv1.EnrollmentPurpose_ENROLLMENT_PURPOSE_UNKNOWN, &header.UnrecognizedCodes)
				header.Action = lookup(s, 8, actionCodes, eligibilityv1.EnrollmentAction_ENROLLMENT_ACTION_UNKNOWN, &header.UnrecognizedCodes)
				if d, ok := parseDate(s.E(3)); ok {
					header.Date = d
				} else if s.E(3) != "" {
					bad(s, 3, "8", "%q is not a valid date", s.E(3))
				}
			case "REF":
				if r := ref(s); r != nil {
					header.Refs = append(header.Refs, r)
				}
			case "DTP":
				if d := dtp(s); d != nil {
					header.Dates = append(header.Dates, d)
				}
			case "N1":
				required(s, 1, 2)
				p := &eligibilityv1.EnrollmentParty{EntityCode: s.E(1), Name: s.E(2), IdQualifier: s.E(3), Id: s.E(4)}
				switch s.E(1) {
				case "P5":
					header.Sponsor = p
				case "IN":
					header.Payer = p
				default:
					header.Others = append(header.Others, p)
				}
			default:
				header.UnmappedSegments = append(header.UnmappedSegments, unmapped(s))
			}

		case s.ID() == "HD":
			required(s, 1, 3)
			hd = &eligibilityv1.HealthCoverage{
				MaintenanceType:   lookup(s, 1, maintenanceTypes, unknownMaint, &cur.Txn.UnrecognizedCodes),
				InsuranceLineCode: s.E(3), PlanCoverageDescription: s.E(4), CoverageLevelCode: s.E(5),
			}
			cur.Txn.Coverages = append(cur.Txn.Coverages, hd)

		case hd != nil:
			// Inside a coverage loop.
			switch s.ID() {
			case "DTP":
				if d := dtp(s); d != nil {
					hd.Dates = append(hd.Dates, d)
				}
			case "REF":
				if r := ref(s); r != nil {
					hd.Refs = append(hd.Refs, r)
				}
			default:
				cur.Txn.UnmappedSegments = append(cur.Txn.UnmappedSegments, unmapped(s))
			}

		default:
			// The member loop, before any coverage.
			t := cur.Txn
			switch {
			case s.ID() == "REF":
				if r := ref(s); r != nil {
					t.Refs = append(t.Refs, r)
					if r.GetQualifier() == "0F" {
						hasSub[cur] = true
					}
				}
			case s.ID() == "DTP":
				if d := dtp(s); d != nil {
					t.Dates = append(t.Dates, d)
				}
			case s.ID() == "NM1":
				nm1 = s.E(1)
				if nm1 != "IL" {
					t.UnmappedSegments = append(t.UnmappedSegments, unmapped(s))
					continue
				}
				hasN[cur] = true
				required(s, 1, 2, 3)
				t.Name = &commonv1.PersonName{Last: s.E(3), First: s.E(4), Middle: s.E(5), Suffix: s.E(7)}
				t.MemberIdQualifier, t.MemberId = s.E(8), s.E(9)
			case nm1 == "IL" && s.ID() == "PER":
				for el := 3; el <= 7; el += 2 {
					if s.E(el) != "" {
						t.Contacts = append(t.Contacts, &eligibilityv1.QualifiedRef{Qualifier: s.E(el), Value: s.E(el + 1)})
					}
				}
			case nm1 == "IL" && s.ID() == "N3":
				if t.Address == nil {
					t.Address = &commonv1.Address{}
				}
				for _, l := range []string{s.E(1), s.E(2)} {
					if l != "" {
						t.Address.Lines = append(t.Address.Lines, l)
					}
				}
			case nm1 == "IL" && s.ID() == "N4":
				if t.Address == nil {
					t.Address = &commonv1.Address{}
				}
				t.Address.City, t.Address.State, t.Address.PostalCode, t.Address.CountryCode = s.E(1), s.E(2), s.E(3), s.E(4)
			case nm1 == "IL" && s.ID() == "DMG":
				if s.E(1) == "D8" && s.E(2) != "" {
					d, ok := parseDate(s.E(2))
					if !ok {
						bad(s, 2, "8", "birth date %q is not a valid date", s.E(2))
					}
					t.BirthDate = d
				}
				t.GenderCode = s.E(3)
			default:
				t.UnmappedSegments = append(t.UnmappedSegments, unmapped(s))
			}
		}
		last = s.Pos
	}
	finish(last)

	if !sawBGN {
		errs = append(errs, x12ack.Missing(set.ST.Pos+1, "BGN", "the transaction set has no BGN"))
	}
	if header.GetSponsor() == nil {
		errs = append(errs, x12ack.Missing(set.ST.Pos+1, "N1", "the transaction set has no N1*P5 sponsor"))
	}
	if header.GetPayer() == nil {
		errs = append(errs, x12ack.Missing(set.ST.Pos+1, "N1", "the transaction set has no N1*IN payer"))
	}
	if len(out) == 0 {
		errs = append(errs, x12ack.Missing(set.ST.Pos+1, "INS", "the transaction set has no member loops"))
	}
	return out, errs
}

func parseDate(s string) (*commonv1.Date, bool) {
	t, err := time.Parse("20060102", s)
	if err != nil {
		return nil, false
	}
	return &commonv1.Date{Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day())}, true
}

// describe is a short reading of a member loop, for logs.
func describe(t *eligibilityv1.EnrollmentTransaction) string {
	return fmt.Sprintf("%s %s (%s, %s)", t.GetName().GetFirst(), t.GetName().GetLast(), t.GetRelationshipCode(),
		strings.ToLower(strings.TrimPrefix(t.GetMaintenanceType().String(), "MAINTENANCE_TYPE_")))
}
