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
	"coreadmin/internal/common/x12"
	"coreadmin/internal/common/x12ack"
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

// seg is a segment being mapped. It notes which elements were read, so
// that a segment with anything left over is kept in the claim's
// unmapped_segments and nothing a provider sends is lost.
type seg struct {
	x12.Segment
	d    x12.Delims
	read []bool
}

// e reads element n.
func (s *seg) e(n int) string { s.skip(n); return s.E(n) }

// c reads component k of element n.
func (s *seg) c(n, k int) string { s.skip(n); return s.C(s.d, n, k) }

// skip marks elements as accounted for: qualifiers that only say how to
// read another element, and elements the guide fixes to one value.
func (s *seg) skip(ns ...int) {
	for _, n := range ns {
		if n < len(s.read) {
			s.read[n] = true
		}
	}
}

// money reads element n as an amount; an unreadable one is left unread.
func (s *seg) money(n int) *commonv1.Money {
	cents, ok := parseCents(s.E(n))
	if !ok {
		return nil
	}
	s.skip(n)
	return &commonv1.Money{Cents: cents}
}

// date reads element n as a CCYYMMDD date; an unreadable one is left
// unread.
func (s *seg) date(n int) *commonv1.Date {
	d, ok := parseDate(s.E(n))
	if !ok {
		return nil
	}
	s.skip(n)
	return d
}

// leftover reports whether any element that was sent has not been read.
func (s *seg) leftover() bool {
	for i := 1; i < len(s.El); i++ {
		if s.El[i] != "" && !s.read[i] {
			return true
		}
	}
	return false
}

// mapper walks one 837P transaction set, tracking which loop each segment
// is in.
type mapper struct {
	d    x12.Delims
	out  []ParsedClaim
	errs []x12ack.Error

	// HL03 of the current HL (20, 22 or 23; empty in the header) and the
	// guide's name for the current loop.
	level, loop string

	// What sits above the claims and is copied onto each.
	txn             *claimsv1.SubmittedTransaction
	chReceived      *timestamppb.Timestamp
	billing         *claimsv1.ProviderRef
	payToAddress    *commonv1.Address
	payToPlan       *claimsv1.SubmittedPayer
	coverage        *claimsv1.SubmittedCoverage
	payer           *claimsv1.SubmittedPayer
	billingPayerIDs []*claimsv1.SubmittedId
	// The 2010BA subscriber, and the 2010CA patient when the claims are
	// under a 2000C patient loop.
	sub, patient *claimsv1.SubmittedPerson
	// Unmapped segments above the claim, by the level they were sent at.
	above map[string][]string

	cur              *ParsedClaim
	line             *claimsv1.ClaimLine
	claimPOS         string
	claimDOS         *commonv1.DateRange
	lastClaimSegment int

	// Where the segments that follow an NM1 go: its address (N3, N4),
	// secondary identifiers (REF) and contacts (PER).
	address  func() *commonv1.Address
	ids      func(*claimsv1.SubmittedId)
	contacts func(*claimsv1.SubmittedContact)
	prov     *claimsv1.ProviderRef
	// The other payer (loop 2320), the other payer's line adjudication
	// (2430) and the form (2440) being read.
	other   *claimsv1.OtherInsurance
	lineAdj *claimsv1.OtherPayerLineAdjudication
	form    *claimsv1.FormIdentification
}

// Map837P maps one transaction set. It returns the claims and the errors
// that make the set unreadable; with any errors the set is rejected and
// the claims are discarded.
//
// Every segment and element the guide defines is mapped to a field of the
// Claim. A segment the mapper does not know, or one carrying an element it
// did not read, is kept verbatim in the claim's unmapped_segments.
func Map837P(d x12.Delims, set *x12.Set) ([]ParsedClaim, []x12ack.Error) {
	m := &mapper{
		d: d, txn: &claimsv1.SubmittedTransaction{}, above: map[string][]string{},
		billing: &claimsv1.ProviderRef{}, sub: &claimsv1.SubmittedPerson{},
	}
	for _, raw := range set.Body {
		s := &seg{Segment: raw, d: d, read: make([]bool, len(raw.El))}
		if m.cur != nil && s.ID() != "HL" {
			m.lastClaimSegment = s.Pos
		}
		if !m.handle(s) || s.leftover() {
			text := s.String(d)
			if m.cur != nil {
				m.cur.Claim.UnmappedSegments = append(m.cur.Claim.UnmappedSegments, text)
			} else {
				m.above[m.level] = append(m.above[m.level], text)
			}
		}
	}
	m.finish()
	if m.chReceived == nil {
		m.missing(set.ST, "BHT", "transaction set has no BHT")
	}
	return m.out, m.errs
}

func (m *mapper) bad(s *seg, el int, code, format string, args ...any) {
	m.errs = append(m.errs, x12ack.Bad(s.Segment, el, code, format, args...))
}

func (m *mapper) missing(s x12.Segment, id, format string, args ...any) {
	m.errs = append(m.errs, x12ack.Missing(s.Pos, id, format, args...))
}

// enter moves to a loop; the segments after an NM1 no longer belong to
// the previous one.
func (m *mapper) enter(loop string) {
	m.loop, m.address, m.ids, m.contacts, m.prov = loop, nil, nil, nil, nil
}

// finish closes the claim being read: its lines take the claim's place of
// service and service date where they sent none, and the claim's service
// dates are the span of its lines'.
func (m *mapper) finish() {
	if m.cur == nil {
		return
	}
	cur := m.cur
	c := cur.Claim
	cur.Last = m.lastClaimSegment
	if len(c.GetLines()) == 0 {
		m.missing(x12.Segment{Pos: cur.First}, "LX", "claim %s has no service lines", c.GetPatientControlNumber())
	}
	var start, end *commonv1.Date
	for _, l := range c.GetLines() {
		if l.GetServiceDates() == nil {
			if m.claimDOS == nil {
				m.missing(x12.Segment{Pos: cur.First}, "DTP", "claim %s line %d has no service date", c.GetPatientControlNumber(), l.GetLineNumber())
				continue
			}
			l.ServiceDates = m.claimDOS
		}
		if l.GetPlaceOfServiceCode() == "" {
			l.PlaceOfServiceCode = m.claimPOS
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
	m.out = append(m.out, *cur)
	m.cur, m.line, m.claimDOS, m.claimPOS = nil, nil, nil, ""
	m.other, m.lineAdj, m.form = nil, nil, nil
}

// handle maps one segment. It returns false for a segment it has no place
// for.
func (m *mapper) handle(s *seg) bool {
	c := m.claim()
	switch s.ID() {
	case "BHT":
		// BHT04/05: when the clearinghouse created this transaction,
		// which is when it accepted the claims in it.
		s.skip(1) // 0019: the 837's hierarchical structure
		m.txn.PurposeCode, m.txn.ReferenceId = s.e(2), s.e(3)
		if t, err := time.Parse("20060102 1504", s.e(4)+" "+padTime(s.e(5))); err == nil {
			m.chReceived = timestamppb.New(t)
		} else {
			m.bad(s, 4, "8", "BHT04 %q is not a date", s.E(4))
		}
		m.txn.TypeCode = s.e(6)
		m.txn.Type = transactionType(m.txn.TypeCode)
		return true

	case "HL":
		m.finish()
		s.skip(1, 2, 4) // the hierarchy's own numbering
		m.level = s.e(3)
		switch m.level {
		case "20":
			m.enter("2000A")
			m.billing, m.payToAddress, m.payToPlan = &claimsv1.ProviderRef{}, nil, nil
			delete(m.above, "20")
			delete(m.above, "22")
			delete(m.above, "23")
		case "22":
			m.enter("2000B")
			m.sub, m.patient = &claimsv1.SubmittedPerson{}, nil
			m.coverage, m.payer, m.billingPayerIDs = nil, nil, nil
			delete(m.above, "22")
			delete(m.above, "23")
		case "23":
			m.enter("2000C")
			m.patient = &claimsv1.SubmittedPerson{}
			delete(m.above, "23")
		default:
			m.bad(s, 3, "7", "HL03 %q is not a hierarchical level of the 837P", s.E(3))
		}
		return true

	case "NM1":
		return m.nm1(s)

	case "N3":
		if m.address == nil {
			return false
		}
		a := m.address()
		for _, l := range []string{s.e(1), s.e(2)} {
			if l != "" {
				a.Lines = append(a.Lines, l)
			}
		}
		return true

	case "N4":
		if m.address == nil {
			return false
		}
		a := m.address()
		a.City, a.State, a.PostalCode, a.CountryCode = s.e(1), s.e(2), s.e(3), s.e(4)
		return true

	case "PER":
		if m.contacts == nil {
			return false
		}
		s.skip(1) // IC: information contact
		ct := &claimsv1.SubmittedContact{Name: s.e(2)}
		for n := 3; n <= 7; n += 2 {
			if s.E(n) != "" || s.E(n+1) != "" {
				ct.Numbers = append(ct.Numbers, &claimsv1.ContactNumber{Qualifier: s.e(n), Value: s.e(n + 1)})
			}
		}
		m.contacts(ct)
		return true

	case "DMG":
		s.skip(1) // D8
		born, ok := parseDate(s.e(2))
		if !ok {
			m.bad(s, 2, "8", "DMG02 %q is not a date", s.E(2))
		}
		switch m.loop {
		case "2010BA":
			m.sub.BirthDate, m.sub.GenderCode = born, s.e(3)
		case "2010CA":
			m.patient.BirthDate, m.patient.GenderCode = born, s.e(3)
		default:
			return false
		}
		return true

	case "PRV":
		s.skip(2) // PXC: the code is a taxonomy code
		switch {
		case m.loop == "2000A" && s.e(1) == "BI":
			m.billing.TaxonomyCode = s.e(3)
		case (m.loop == "2310B" || m.loop == "2420A") && s.e(1) == "PE":
			m.prov.TaxonomyCode = s.e(3)
		default:
			return false
		}
		return true

	case "SBR":
		if c == nil {
			// Loop 2000B: the coverage the claims below are billed under.
			m.coverage = &claimsv1.SubmittedCoverage{
				PayerResponsibilityCode: s.e(1), PayerResponsibility: payerResponsibility(s.e(1)),
				GroupNumber: s.e(3), GroupName: s.e(4), InsuranceTypeCode: s.e(5), ClaimFilingIndicatorCode: s.e(9),
			}
			m.sub.RelationshipCode, m.sub.Relationship = s.e(2), relationship(s.e(2))
			return true
		}
		if m.line != nil {
			return false
		}
		// Loop 2320: another payer on the claim.
		m.enter("2320")
		m.other = &claimsv1.OtherInsurance{
			PayerResponsibilityCode: s.e(1), PayerResponsibility: payerResponsibility(s.e(1)),
			RelationshipCode: s.e(2), Relationship: relationship(s.e(2)),
			GroupNumber: s.e(3), GroupName: s.e(4), InsuranceTypeCode: s.e(5), ClaimFilingIndicatorCode: s.e(9),
		}
		c.OtherInsurance = append(c.OtherInsurance, m.other)
		return true

	case "PAT":
		var p *claimsv1.SubmittedPerson
		switch m.loop {
		case "2000B":
			p = m.sub
		case "2000C":
			p = m.patient
			p.RelationshipCode, p.Relationship = s.e(1), relationship(s.e(1))
		default:
			return false
		}
		if s.E(5) == "D8" {
			s.skip(5)
			p.DeathDate = s.date(6)
		}
		if s.E(7) == "01" { // pounds
			s.skip(7)
			p.WeightPounds = s.e(8)
		}
		p.PregnancyIndicator = s.e(9)
		return true

	case "REF":
		return m.ref(s)

	case "CLM":
		m.finish()
		if m.level != "22" && m.level != "23" {
			m.bad(s, 0, "", "CLM is not under a subscriber or patient")
			s.skip(1, 2, 5, 6, 7, 8, 9, 10, 11, 12, 20)
			return true
		}
		m.enter("2300")
		m.cur = &ParsedClaim{First: s.Pos}
		m.lastClaimSegment = s.Pos
		b := proto.Clone(m.billing).(*claimsv1.ProviderRef)
		b.SecondaryIds = append(b.SecondaryIds, m.billingPayerIDs...)
		c = &claimsv1.Claim{
			Type:                      claimsv1.ClaimType_CLAIM_TYPE_PROFESSIONAL,
			Source:                    claimsv1.ClaimSource_CLAIM_SOURCE_EDI_837,
			PatientControlNumber:      s.e(1),
			MemberCardId:              m.sub.GetMemberNumber(),
			BillingProvider:           b,
			ClearinghouseReceivedTime: m.chReceived,
			SubmittedSubscriber:       proto.Clone(m.sub).(*claimsv1.SubmittedPerson),
			SubmittedTransaction:      proto.Clone(m.txn).(*claimsv1.SubmittedTransaction),
		}
		if m.patient != nil {
			c.SubmittedPatient = proto.Clone(m.patient).(*claimsv1.SubmittedPerson)
		}
		if m.coverage != nil {
			c.SubmittedCoverage = proto.Clone(m.coverage).(*claimsv1.SubmittedCoverage)
		}
		if m.payer != nil {
			c.SubmittedPayer = proto.Clone(m.payer).(*claimsv1.SubmittedPayer)
		}
		if m.payToAddress != nil {
			c.PayToAddress = proto.Clone(m.payToAddress).(*commonv1.Address)
		}
		if m.payToPlan != nil {
			c.PayToPlan = proto.Clone(m.payToPlan).(*claimsv1.SubmittedPayer)
		}
		for _, level := range []string{"", "20", "22", "23"} {
			if level != "23" || m.level == "23" {
				c.UnmappedSegments = append(c.UnmappedSegments, m.above[level]...)
			}
		}
		m.cur.Claim = c
		if s.E(1) == "" {
			m.bad(s, 1, "1", "CLM01 patient control number is missing")
		}
		if total, ok := parseCents(s.e(2)); ok {
			c.TotalCharge = &commonv1.Money{Cents: total}
		} else {
			m.bad(s, 2, "1", "CLM02 total charge %q is missing or not an amount", s.E(2))
		}
		// CLM05: place of service, a fixed qualifier (B), frequency.
		m.claimPOS = s.c(5, 1)
		c.PlaceOfServiceCode = m.claimPOS
		switch s.c(5, 3) {
		case "1":
			c.Frequency = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_ORIGINAL
		case "7":
			c.Frequency = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT
		case "8":
			c.Frequency = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID
		default:
			m.bad(s, 5, "7", "CLM05-3 claim frequency %q is not 1, 7 or 8", s.c(5, 3))
		}
		ind := &claimsv1.ClaimIndicators{
			ProviderSignatureOnFile: s.e(6),
			AcceptAssignmentCode:    s.e(7), AcceptAssignment: acceptAssignment(s.e(7)),
			BenefitsAssignmentCode: s.e(8), BenefitsAssignment: benefitsAssignment(s.e(8)),
			ReleaseOfInformationCode: s.e(9), PatientSignatureSourceCode: s.e(10),
			SpecialProgramCode: s.e(12), DelayReasonCode: s.e(20),
		}
		if proto.Size(ind) > 0 {
			c.Indicators = ind
		}
		// CLM11: up to three related causes, then the accident's state
		// and country.
		rc := &claimsv1.RelatedCauses{AutoAccidentState: s.c(11, 4), AutoAccidentCountryCode: s.c(11, 5)}
		for k := 1; k <= 3; k++ {
			if code := s.c(11, k); code != "" {
				rc.CauseCodes = append(rc.CauseCodes, code)
				rc.Causes = append(rc.Causes, relatedCause(code))
			}
		}
		if proto.Size(rc) > 0 {
			c.RelatedCauses = rc
		}
		return true

	case "DTP":
		return m.dtp(s)

	case "HI":
		if c == nil {
			return false
		}
		for i := 1; i < len(s.El); i++ {
			code := s.c(i, 2)
			if code == "" {
				continue
			}
			switch q := s.c(i, 1); q {
			case "BP", "BO":
				c.AnesthesiaProcedureCodes = append(c.AnesthesiaProcedureCodes, code)
			case "BG":
				c.ConditionCodes = append(c.ConditionCodes, code)
			default:
				c.DiagnosisCodes = append(c.DiagnosisCodes, code)
				c.DiagnosisCodeQualifiers = append(c.DiagnosisCodeQualifiers, q)
			}
		}
		return true

	case "PWK":
		if c == nil {
			return false
		}
		if s.E(5) == "AC" { // the next element is an attachment control number
			s.skip(5)
		}
		a := &claimsv1.Attachment{ReportTypeCode: s.e(1), TransmissionCode: s.e(2), ControlNumber: s.e(6)}
		if m.line != nil {
			m.line.Attachments = append(m.line.Attachments, a)
		} else {
			c.Attachments = append(c.Attachments, a)
		}
		return true

	case "CN1":
		if c == nil {
			return false
		}
		ci := &claimsv1.ContractInformation{TypeCode: s.e(1), Amount: s.money(2), Percent: s.e(3), Code: s.e(4), TermsDiscountPercent: s.e(5), Version: s.e(6)}
		if m.line != nil {
			m.line.Contract = ci
		} else {
			c.Contract = ci
		}
		return true

	case "AMT":
		amount := func() *commonv1.Money { return s.money(2) }
		switch q := s.e(1); {
		case c == nil:
			return false
		case m.loop == "2430" && q == "EAF":
			m.lineAdj.RemainingPatientLiability = amount()
		case m.line != nil && m.loop == "2400" && q == "T":
			m.line.SalesTax = amount()
		case m.line != nil && m.loop == "2400" && q == "F4":
			m.line.PostageClaimed = amount()
		case m.line == nil && m.other != nil && q == "D":
			m.other.PayerPaid = amount()
		case m.line == nil && m.other != nil && q == "A8":
			m.other.NonCovered = amount()
		case m.line == nil && m.other != nil && q == "EAF":
			m.other.RemainingPatientLiability = amount()
		case m.loop == "2300" && q == "F5":
			c.PatientPaid = amount()
		default:
			return false
		}
		return true

	case "K3":
		switch {
		case c == nil:
			return false
		case m.line != nil:
			m.line.FileInformation = append(m.line.FileInformation, s.e(1))
		default:
			c.FileInformation = append(c.FileInformation, s.e(1))
		}
		return true

	case "NTE":
		if c == nil {
			return false
		}
		n := &claimsv1.SubmittedNote{ReferenceCode: s.e(1), Text: s.e(2)}
		if m.line != nil {
			m.line.Notes = append(m.line.Notes, n)
		} else {
			c.Notes = append(c.Notes, n)
		}
		return true

	case "CR1":
		if c == nil {
			return false
		}
		if s.E(1) == "LB" {
			s.skip(1)
		}
		if s.E(5) == "DH" { // miles
			s.skip(5)
		}
		a := &claimsv1.AmbulanceTransport{PatientWeightPounds: s.e(2), TransportReasonCode: s.e(4), Miles: s.e(6), RoundTripPurpose: s.e(9), StretcherPurpose: s.e(10)}
		if m.line != nil {
			m.line.AmbulanceTransport = a
		} else {
			c.AmbulanceTransport = a
		}
		return true

	case "CR2":
		if c == nil || m.line != nil {
			return false
		}
		sm := &claimsv1.SpinalManipulation{PatientConditionCode: s.e(8)}
		for _, d := range []string{s.e(10), s.e(11)} {
			if d != "" {
				sm.Descriptions = append(sm.Descriptions, d)
			}
		}
		c.SpinalManipulation = sm
		return true

	case "CR3":
		if m.line == nil {
			return false
		}
		if s.E(2) == "MO" {
			s.skip(2)
		}
		m.line.DmeCertification = &claimsv1.DmeCertification{TypeCode: s.e(1), Months: s.e(3)}
		return true

	case "CRC":
		if c == nil {
			return false
		}
		ci := &claimsv1.ConditionInformation{CategoryCode: s.e(1), Applies: s.e(2)}
		for n := 3; n <= 7; n++ {
			if code := s.e(n); code != "" {
				ci.ConditionCodes = append(ci.ConditionCodes, code)
			}
		}
		if m.line != nil {
			m.line.Conditions = append(m.line.Conditions, ci)
		} else {
			c.Conditions = append(c.Conditions, ci)
		}
		return true

	case "HCP":
		if c == nil {
			return false
		}
		r := &claimsv1.Repricing{
			MethodologyCode: s.e(1), Allowed: s.money(2), Savings: s.money(3), OrganizationId: s.e(4), Rate: s.e(5),
			ApprovedApgCode: s.e(6), ApprovedApgAmount: s.money(7),
			ApprovedProcedureQualifier: s.e(9), ApprovedProcedureCode: s.e(10), ApprovedUnitBasisCode: s.e(11), ApprovedUnits: s.e(12),
			RejectReasonCode: s.e(13), PolicyComplianceCode: s.e(14), ExceptionCode: s.e(15),
		}
		if m.line != nil {
			m.line.Repricing = r
		} else {
			c.Repricing = r
		}
		return true

	case "CAS":
		var to *[]*claimsv1.SubmittedAdjustment
		switch {
		case m.loop == "2430":
			to = &m.lineAdj.Adjustments
		case m.line == nil && m.other != nil:
			to = &m.other.Adjustments
		default:
			return false
		}
		// CAS01 is the group; then up to six of reason, amount, quantity.
		group := s.e(1)
		for n := 2; n <= 17; n += 3 {
			if s.E(n) == "" && s.E(n+1) == "" {
				continue
			}
			*to = append(*to, &claimsv1.SubmittedAdjustment{GroupCode: group, ReasonCode: s.e(n), Amount: s.money(n + 1), Quantity: s.e(n + 2)})
		}
		return true

	case "OI":
		if m.other == nil || m.line != nil {
			return false
		}
		m.other.BenefitsAssignmentCode, m.other.BenefitsAssignment = s.e(3), benefitsAssignment(s.e(3))
		m.other.PatientSignatureSourceCode, m.other.ReleaseOfInformationCode = s.e(4), s.e(6)
		return true

	case "MOA":
		if m.other == nil || m.line != nil {
			return false
		}
		moa := &claimsv1.MedicareOutpatientAdjudication{ReimbursementRate: s.e(1), HcpcsPayable: s.money(2), EsrdPayment: s.money(8), NonPayableProfessionalComponent: s.money(9)}
		for n := 3; n <= 7; n++ {
			if code := s.e(n); code != "" {
				moa.RemarkCodes = append(moa.RemarkCodes, code)
			}
		}
		m.other.MedicareOutpatient = moa
		return true

	case "LX":
		if c == nil {
			m.bad(s, 0, "", "LX outside a claim")
			s.skip(1)
			return true
		}
		m.enter("2400")
		m.other, m.lineAdj, m.form = nil, nil, nil
		m.line = &claimsv1.ClaimLine{}
		n, err := strconv.Atoi(s.e(1))
		if err != nil {
			m.bad(s, 1, "6", "LX01 %q is not a line number", s.E(1))
		}
		m.line.LineNumber = int32(n)
		c.Lines = append(c.Lines, m.line)
		return true

	case "SV1":
		line := m.line
		if line == nil {
			m.bad(s, 0, "", "SV1 without LX")
			s.skip(1, 2, 3, 4, 5, 7, 9, 11, 12, 15)
			return true
		}
		// SV101: qualifier, code, up to four modifiers, description.
		if s.c(1, 1) != "HC" || s.c(1, 2) == "" {
			m.bad(s, 1, "1", "SV101 has no HCPCS/CPT procedure code")
		}
		line.ProcedureCodeQualifier, line.ProcedureCode, line.ProcedureDescription = s.c(1, 1), s.c(1, 2), s.c(1, 7)
		for k := 3; k <= 6; k++ {
			if mod := s.c(1, k); mod != "" {
				line.Modifiers = append(line.Modifiers, mod)
			}
		}
		if charge, ok := parseCents(s.e(2)); ok {
			line.Charge = &commonv1.Money{Cents: charge}
		} else {
			m.bad(s, 2, "1", "SV102 line charge %q is missing or not an amount", s.E(2))
		}
		line.UnitBasisCode = s.e(3)
		if units, err := strconv.ParseFloat(s.e(4), 64); err == nil {
			line.UnitsMilli = int64(units*1000 + 0.5)
		} else {
			m.bad(s, 4, "6", "SV104 units %q is not a number", s.E(4))
		}
		line.PlaceOfServiceCode = s.e(5)
		for k := 1; k <= 4; k++ {
			if p, err := strconv.Atoi(s.c(7, k)); err == nil {
				line.DiagnosisPointers = append(line.DiagnosisPointers, int32(p))
			}
		}
		line.EmergencyIndicator, line.EpsdtIndicator, line.FamilyPlanningIndicator, line.CopayStatusCode = s.e(9), s.e(11), s.e(12), s.e(15)
		return true

	case "SV5":
		if m.line == nil {
			return false
		}
		if s.E(2) == "DA" { // days
			s.skip(2)
		}
		m.line.Dme = &claimsv1.DurableMedicalEquipment{ProcedureCode: s.c(1, 2), Days: s.e(3), RentalPrice: s.money(4), PurchasePrice: s.money(5), RentalFrequencyCode: s.e(6)}
		return true

	case "QTY":
		switch q := s.e(1); {
		case m.line == nil:
			return false
		case q == "PT":
			m.line.AmbulancePatientCount = s.e(2)
		case q == "FL":
			m.line.ObstetricAnesthesiaUnits = s.e(2)
		default:
			return false
		}
		return true

	case "MEA":
		if m.line == nil {
			return false
		}
		m.line.TestResults = append(m.line.TestResults, &claimsv1.TestResult{ReferenceCode: s.e(1), MeasurementCode: s.e(2), Value: s.e(3)})
		return true

	case "PS1":
		if m.line == nil {
			return false
		}
		m.line.PurchasedService = &claimsv1.PurchasedService{ProviderId: s.e(1), Charge: s.money(2)}
		return true

	case "LIN":
		if m.line == nil {
			return false
		}
		m.enter("2410")
		if s.E(2) == "N4" { // the id is an NDC
			s.skip(2)
			m.line.Ndc = s.e(3)
		}
		return true

	case "CTP":
		if m.loop != "2410" {
			return false
		}
		d := m.drug()
		d.Quantity, d.UnitCode = s.e(4), s.c(5, 1)
		return true

	case "SVD":
		if m.line == nil {
			return false
		}
		m.enter("2430")
		// SVD03: qualifier, code, up to four modifiers, description.
		m.lineAdj = &claimsv1.OtherPayerLineAdjudication{PayerId: s.e(1), Paid: s.money(2), ProcedureCode: s.c(3, 2), ProcedureDescription: s.c(3, 7), PaidUnits: s.e(5)}
		for k := 3; k <= 6; k++ {
			if mod := s.c(3, k); mod != "" {
				m.lineAdj.Modifiers = append(m.lineAdj.Modifiers, mod)
			}
		}
		if n, err := strconv.Atoi(s.E(6)); err == nil {
			s.skip(6)
			m.lineAdj.BundledLineNumber = int32(n)
		}
		m.line.OtherPayerAdjudications = append(m.line.OtherPayerAdjudications, m.lineAdj)
		return true

	case "LQ":
		if m.line == nil {
			return false
		}
		m.enter("2440")
		m.form = &claimsv1.FormIdentification{Qualifier: s.e(1), FormNumber: s.e(2)}
		m.line.Forms = append(m.line.Forms, m.form)
		return true

	case "FRM":
		if m.loop != "2440" {
			return false
		}
		m.form.Answers = append(m.form.Answers, &claimsv1.FormAnswer{Question: s.e(1), YesNo: s.e(2), Text: s.e(3), Date: s.date(4), Percent: s.e(5)})
		return true
	}
	return false
}

// claim is the claim being read, or nil above the first CLM.
func (m *mapper) claim() *claimsv1.Claim {
	if m.cur == nil {
		return nil
	}
	return m.cur.Claim
}

func (m *mapper) drug() *claimsv1.LineDrug {
	if m.line.Drug == nil {
		m.line.Drug = &claimsv1.LineDrug{}
	}
	return m.line.Drug
}

// provider starts a provider's loop: its NM1, and where its address,
// identifiers and contacts go.
func (m *mapper) provider(loop string, s *seg, p *claimsv1.ProviderRef) *claimsv1.ProviderRef {
	m.enter(loop)
	p.EntityTypeCode = s.e(2)
	p.Name = strings.TrimSpace(strings.Join([]string{s.e(4), s.e(3)}, " "))
	if p.EntityTypeCode == "1" {
		p.PersonName = &commonv1.PersonName{Last: s.e(3), First: s.e(4), Middle: s.e(5), Suffix: s.e(7)}
	}
	// NM108/109: the NPI, or (outside the guide) some other identifier.
	if q := s.e(8); q == "XX" || q == "" || loop == "2010AA" {
		p.Npi = s.e(9)
	} else {
		p.SecondaryIds = append(p.SecondaryIds, &claimsv1.SubmittedId{Qualifier: q, Value: s.e(9)})
	}
	m.prov = p
	m.address = func() *commonv1.Address {
		if p.Address == nil {
			p.Address = &commonv1.Address{}
		}
		return p.Address
	}
	m.ids = func(id *claimsv1.SubmittedId) { p.SecondaryIds = append(p.SecondaryIds, id) }
	m.contacts = func(ct *claimsv1.SubmittedContact) { p.Contacts = append(p.Contacts, ct) }
	return p
}

// payerOf starts a payer's loop.
func (m *mapper) payerOf(loop string, s *seg) *claimsv1.SubmittedPayer {
	m.enter(loop)
	s.skip(2) // always an organization
	p := &claimsv1.SubmittedPayer{Name: s.e(3), IdQualifier: s.e(8), Id: s.e(9)}
	m.address = func() *commonv1.Address {
		if p.Address == nil {
			p.Address = &commonv1.Address{}
		}
		return p.Address
	}
	m.ids = func(id *claimsv1.SubmittedId) { p.SecondaryIds = append(p.SecondaryIds, id) }
	return p
}

// person starts a subscriber's or patient's loop.
func (m *mapper) person(loop string, s *seg, p *claimsv1.SubmittedPerson) {
	m.enter(loop)
	s.skip(2)
	p.Name = &commonv1.PersonName{Last: s.e(3), First: s.e(4), Middle: s.e(5), Suffix: s.e(7)}
	m.address = func() *commonv1.Address {
		if p.Address == nil {
			p.Address = &commonv1.Address{}
		}
		return p.Address
	}
	m.contacts = func(ct *claimsv1.SubmittedContact) { p.Contact = ct }
}

// location starts an ambulance pick-up or drop-off loop.
func (m *mapper) location(loop string, s *seg) *claimsv1.AmbulanceLocation {
	m.enter(loop)
	s.skip(2)
	l := &claimsv1.AmbulanceLocation{Name: s.e(3)}
	m.address = func() *commonv1.Address {
		if l.Address == nil {
			l.Address = &commonv1.Address{}
		}
		return l.Address
	}
	return l
}

// nm1 maps a name segment, which opens a loop.
func (m *mapper) nm1(s *seg) bool {
	c := m.claim()
	code := s.e(1)
	switch {
	case c == nil:
		switch code {
		case "41": // 1000A submitter
			m.enter("1000A")
			s.skip(2, 8) // 46: the id is an ETIN
			m.txn.SubmitterName, m.txn.SubmitterId = strings.TrimSpace(s.e(3)+" "+s.e(4)), s.e(9)
			m.contacts = func(ct *claimsv1.SubmittedContact) { m.txn.SubmitterContacts = append(m.txn.SubmitterContacts, ct) }
		case "40": // 1000B receiver
			m.enter("1000B")
			s.skip(2, 8)
			m.txn.ReceiverName, m.txn.ReceiverId = s.e(3), s.e(9)
		case "85": // 2010AA billing provider
			m.provider("2010AA", s, m.billing)
			if s.E(8) != "XX" || len(s.E(9)) != 10 {
				m.bad(s, 9, "7", "billing provider NPI %q is not a 10-digit NPI", s.E(9))
			}
		case "87": // 2010AB pay-to address
			m.enter("2010AB")
			s.skip(2)
			m.address = func() *commonv1.Address {
				if m.payToAddress == nil {
					m.payToAddress = &commonv1.Address{}
				}
				return m.payToAddress
			}
		case "PE": // 2010AC pay-to plan
			m.payToPlan = m.payerOf("2010AC", s)
		case "IL": // 2010BA subscriber
			m.person("2010BA", s, m.sub)
			m.sub.MemberNumberQualifier, m.sub.MemberNumber = s.e(8), s.e(9)
			if s.E(9) == "" {
				m.bad(s, 9, "1", "subscriber has no member id")
			}
		case "PR": // 2010BB payer
			m.payer = m.payerOf("2010BB", s)
		case "QC": // 2010CA patient
			if m.patient == nil {
				return false
			}
			m.person("2010CA", s, m.patient)
		default:
			return false
		}

	case m.line == nil && m.other != nil:
		// Loop 2320: the other payer's subscriber, the payer, and the
		// claim's providers as that payer identifies them.
		o := m.other
		switch code {
		case "IL": // 2330A
			m.enter("2330A")
			s.skip(2, 8)
			o.SubscriberName = &commonv1.PersonName{Last: s.e(3), First: s.e(4), Middle: s.e(5), Suffix: s.e(7)}
			o.SubscriberId = s.e(9)
			m.address = func() *commonv1.Address {
				if o.SubscriberAddress == nil {
					o.SubscriberAddress = &commonv1.Address{}
				}
				return o.SubscriberAddress
			}
		case "PR": // 2330B
			o.Payer = m.payerOf("2330B", s)
		default: // 2330C to G
			m.enter("2330C")
			s.skip(2)
			p := &claimsv1.OtherPayerProvider{RoleCode: code}
			o.Providers = append(o.Providers, p)
			m.ids = func(id *claimsv1.SubmittedId) { p.Ids = append(p.Ids, id) }
		}

	case m.line == nil:
		switch code {
		case "DN": // 2310A referring provider
			c.ReferringProvider = m.provider("2310A", s, &claimsv1.ProviderRef{})
		case "P3": // 2310A primary care provider
			c.PrimaryCareProvider = m.provider("2310A", s, &claimsv1.ProviderRef{})
		case "82": // 2310B rendering provider
			c.RenderingProvider = m.provider("2310B", s, &claimsv1.ProviderRef{})
		case "77": // 2310C service facility location
			c.ServiceFacility = m.provider("2310C", s, &claimsv1.ProviderRef{})
		case "DQ": // 2310D supervising provider
			c.SupervisingProvider = m.provider("2310D", s, &claimsv1.ProviderRef{})
		case "PW": // 2310E ambulance pick-up
			c.AmbulancePickup = m.location("2310E", s)
		case "45": // 2310F ambulance drop-off
			c.AmbulanceDropoff = m.location("2310F", s)
		default:
			return false
		}

	default:
		l := m.line
		switch code {
		case "82": // 2420A
			l.RenderingProvider = m.provider("2420A", s, &claimsv1.ProviderRef{})
		case "QB": // 2420B
			l.PurchasedServiceProvider = m.provider("2420B", s, &claimsv1.ProviderRef{})
		case "77": // 2420C
			l.ServiceFacility = m.provider("2420C", s, &claimsv1.ProviderRef{})
		case "DQ": // 2420D
			l.SupervisingProvider = m.provider("2420D", s, &claimsv1.ProviderRef{})
		case "DK": // 2420E
			l.OrderingProvider = m.provider("2420E", s, &claimsv1.ProviderRef{})
		case "DN": // 2420F
			l.ReferringProvider = m.provider("2420F", s, &claimsv1.ProviderRef{})
		case "P3": // 2420F
			l.PrimaryCareProvider = m.provider("2420F", s, &claimsv1.ProviderRef{})
		case "PW": // 2420G
			l.AmbulancePickup = m.location("2420G", s)
		case "45": // 2420H
			l.AmbulanceDropoff = m.location("2420H", s)
		default:
			return false
		}
	}
	return true
}

// ref maps a reference number by the loop it is in.
func (m *mapper) ref(s *seg) bool {
	c := m.claim()
	q, v := s.e(1), s.e(2)
	switch m.loop {
	case "2010AA":
		if q == "EI" || q == "SY" {
			m.billing.Tin, m.billing.TaxIdQualifier = v, q
			return true
		}
	case "2010BA", "2010CA":
		p := m.sub
		if m.loop == "2010CA" {
			p = m.patient
		}
		switch q {
		case "SY":
			p.Ssn = v
		case "Y4":
			p.PropertyCasualtyClaimNumber = v
		default:
			return false
		}
		return true
	case "2010BB":
		// G2 and LU here are the billing provider's ids with this payer.
		if q == "G2" || q == "LU" {
			m.billingPayerIDs = append(m.billingPayerIDs, &claimsv1.SubmittedId{Qualifier: q, Value: v})
			return true
		}
	case "2300":
		refs := c.GetReferences()
		if refs == nil {
			refs = &claimsv1.ClaimReferences{}
		}
		switch q {
		case "F8":
			c.PayerClaimControlNumber = v
			return true
		case "D9":
			c.ClearinghouseTraceNumber = v
			return true
		case "G1":
			c.AuthorizationNumbers = append(c.AuthorizationNumbers, v)
			refs.PriorAuthorizationNumber = v
		case "9F":
			c.AuthorizationNumbers = append(c.AuthorizationNumbers, v)
			refs.ReferralNumber = v
		case "4N":
			refs.ServiceAuthorizationExceptionCode = v
		case "F5":
			refs.MandatoryMedicareCrossover = v
		case "EW":
			refs.MammographyCertificationNumber = v
		case "X4":
			refs.CliaNumber = v
		case "9A":
			refs.RepricedClaimNumber = v
		case "9C":
			refs.AdjustedRepricedClaimNumber = v
		case "LX":
			refs.InvestigationalDeviceExemptionNumber = v
		case "EA":
			refs.MedicalRecordNumber = v
		case "P4":
			refs.DemonstrationProjectId = v
		case "1J":
			refs.CarePlanOversightNumber = v
		default:
			return false
		}
		c.References = refs
		return true
	case "2330A":
		if q == "SY" {
			m.other.SubscriberSsn = v
			return true
		}
		return false
	case "2330B":
		switch q {
		case "G1":
			m.other.PriorAuthorizationNumber = v
		case "9F":
			m.other.ReferralNumber = v
		case "T4":
			m.other.ClaimAdjustmentIndicator = v
		case "F8":
			m.other.ClaimControlNumber = v
		default:
			m.ids(&claimsv1.SubmittedId{Qualifier: q, Value: v})
		}
		return true
	case "2400":
		refs := m.line.GetReferences()
		if refs == nil {
			refs = &claimsv1.LineReferences{}
		}
		switch q {
		case "6R":
			refs.LineItemControlNumber = v
		case "G1":
			refs.PriorAuthorizationNumbers = append(refs.PriorAuthorizationNumbers, v)
		case "9F":
			refs.ReferralNumbers = append(refs.ReferralNumbers, v)
		case "9B":
			refs.RepricedLineItemNumber = v
		case "9D":
			refs.AdjustedRepricedLineItemNumber = v
		case "EW":
			refs.MammographyCertificationNumber = v
		case "X4":
			refs.CliaNumber = v
		case "F4":
			refs.ReferringCliaNumber = v
		case "BT":
			refs.ImmunizationBatchNumber = v
		default:
			return false
		}
		m.line.References = refs
		return true
	case "2410":
		switch q {
		case "XZ":
			m.drug().PrescriptionNumber = v
		case "VY":
			m.drug().LinkSequenceNumber = v
		default:
			return false
		}
		return true
	}
	// Any other REF after an NM1 is one of that party's identifiers.
	if m.ids == nil {
		return false
	}
	m.ids(&claimsv1.SubmittedId{Qualifier: q, Value: v})
	return true
}

// dtp maps a date by its qualifier and the loop it is in.
func (m *mapper) dtp(s *seg) bool {
	c := m.claim()
	if c == nil {
		return false
	}
	q := s.e(1)
	r, ok := parseRange(s.e(2), s.E(3))
	if q == "472" {
		// The service date. A professional claim sends it on each line;
		// one sent on the claim covers lines that have none.
		s.skip(3)
		if !ok {
			m.bad(s, 3, "8", "DTP03 %q is not a date", s.E(3))
			return true
		}
		if m.line != nil {
			m.line.ServiceDates = r
		} else {
			m.claimDOS = r
		}
		return true
	}
	if !ok {
		return false // DTP03 stays unread, so the segment is kept
	}
	s.skip(3)
	day := r.GetStart()
	switch {
	case m.loop == "2430":
		if q != "573" {
			return false
		}
		m.lineAdj.AdjudicationDate = day
	case m.line != nil:
		if m.loop != "2400" {
			return false
		}
		d := m.line.GetDates()
		if d == nil {
			d = &claimsv1.LineDates{}
		}
		switch q {
		case "471":
			d.Prescription = day
		case "607":
			d.CertificationRevision = day
		case "463":
			d.BeginTherapy = day
		case "461":
			d.LastCertification = day
		case "304":
			d.LastSeen = day
		case "738":
			d.HemoglobinTest = day
		case "739":
			d.CreatinineTest = day
		case "011":
			d.Shipped = day
		case "455":
			d.LastXray = day
		case "454":
			d.InitialTreatment = day
		default:
			return false
		}
		m.line.Dates = d
	case m.loop == "2330B":
		if q != "573" {
			return false
		}
		m.other.AdjudicationDate = day
	case m.loop == "2300":
		d := c.GetDates()
		if d == nil {
			d = &claimsv1.ClaimDates{}
		}
		switch q {
		case "431":
			d.Onset = day
		case "454":
			d.InitialTreatment = day
		case "304":
			d.LastSeen = day
		case "453":
			d.AcuteManifestation = day
		case "439":
			d.Accident = day
		case "484":
			d.LastMenstrualPeriod = day
		case "455":
			d.LastXray = day
		case "471":
			d.HearingVisionPrescription = day
		case "314": // the whole period
			d.Disability = r
		case "360": // its first day
			if d.Disability == nil {
				d.Disability = &commonv1.DateRange{}
			}
			d.Disability.Start = day
		case "361": // its last day
			if d.Disability == nil {
				d.Disability = &commonv1.DateRange{}
			}
			d.Disability.End = day
		case "297":
			d.LastWorked = day
		case "296":
			d.ReturnToWork = day
		case "435":
			d.Admission = day
		case "096":
			d.Discharge = day
		case "090":
			d.AssumedCare = day
		case "091":
			d.RelinquishedCare = day
		case "444":
			d.PropertyCasualtyFirstContact = day
		case "050":
			d.RepricerReceived = day
		default:
			return false
		}
		c.Dates = d
	default:
		return false
	}
	return true
}

func transactionType(code string) claimsv1.TransactionType {
	switch code {
	case "":
		return claimsv1.TransactionType_TRANSACTION_TYPE_UNSPECIFIED
	case "CH":
		return claimsv1.TransactionType_TRANSACTION_TYPE_CLAIM
	case "RP":
		return claimsv1.TransactionType_TRANSACTION_TYPE_ENCOUNTER
	case "31":
		return claimsv1.TransactionType_TRANSACTION_TYPE_SUBROGATION_DEMAND
	}
	return claimsv1.TransactionType_TRANSACTION_TYPE_UNKNOWN
}

func payerResponsibility(code string) claimsv1.PayerResponsibility {
	switch code {
	case "":
		return claimsv1.PayerResponsibility_PAYER_RESPONSIBILITY_UNSPECIFIED
	case "P":
		return claimsv1.PayerResponsibility_PAYER_RESPONSIBILITY_PRIMARY
	case "S":
		return claimsv1.PayerResponsibility_PAYER_RESPONSIBILITY_SECONDARY
	case "T":
		return claimsv1.PayerResponsibility_PAYER_RESPONSIBILITY_TERTIARY
	case "A", "B", "C", "D", "E", "F", "G", "H":
		return claimsv1.PayerResponsibility_PAYER_RESPONSIBILITY_FOURTH_OR_LATER
	case "U":
		return claimsv1.PayerResponsibility_PAYER_RESPONSIBILITY_NOT_KNOWN
	}
	return claimsv1.PayerResponsibility_PAYER_RESPONSIBILITY_UNKNOWN
}

func relationship(code string) claimsv1.PatientRelationship {
	switch code {
	case "":
		return claimsv1.PatientRelationship_PATIENT_RELATIONSHIP_UNSPECIFIED
	case "18":
		return claimsv1.PatientRelationship_PATIENT_RELATIONSHIP_SELF
	case "01":
		return claimsv1.PatientRelationship_PATIENT_RELATIONSHIP_SPOUSE
	case "19":
		return claimsv1.PatientRelationship_PATIENT_RELATIONSHIP_CHILD
	case "20":
		return claimsv1.PatientRelationship_PATIENT_RELATIONSHIP_EMPLOYEE
	case "53":
		return claimsv1.PatientRelationship_PATIENT_RELATIONSHIP_LIFE_PARTNER
	case "39":
		return claimsv1.PatientRelationship_PATIENT_RELATIONSHIP_ORGAN_DONOR
	case "40":
		return claimsv1.PatientRelationship_PATIENT_RELATIONSHIP_CADAVER_DONOR
	case "G8":
		return claimsv1.PatientRelationship_PATIENT_RELATIONSHIP_OTHER
	case "21":
		return claimsv1.PatientRelationship_PATIENT_RELATIONSHIP_NOT_KNOWN
	}
	return claimsv1.PatientRelationship_PATIENT_RELATIONSHIP_UNKNOWN
}

func acceptAssignment(code string) claimsv1.AcceptAssignment {
	switch code {
	case "":
		return claimsv1.AcceptAssignment_ACCEPT_ASSIGNMENT_UNSPECIFIED
	case "A":
		return claimsv1.AcceptAssignment_ACCEPT_ASSIGNMENT_ASSIGNED
	case "B":
		return claimsv1.AcceptAssignment_ACCEPT_ASSIGNMENT_CLINICAL_LAB_ONLY
	case "C":
		return claimsv1.AcceptAssignment_ACCEPT_ASSIGNMENT_NOT_ASSIGNED
	}
	return claimsv1.AcceptAssignment_ACCEPT_ASSIGNMENT_UNKNOWN
}

func benefitsAssignment(code string) claimsv1.BenefitsAssignment {
	switch code {
	case "":
		return claimsv1.BenefitsAssignment_BENEFITS_ASSIGNMENT_UNSPECIFIED
	case "Y":
		return claimsv1.BenefitsAssignment_BENEFITS_ASSIGNMENT_YES
	case "N":
		return claimsv1.BenefitsAssignment_BENEFITS_ASSIGNMENT_NO
	case "W":
		return claimsv1.BenefitsAssignment_BENEFITS_ASSIGNMENT_NOT_APPLICABLE
	}
	return claimsv1.BenefitsAssignment_BENEFITS_ASSIGNMENT_UNKNOWN
}

func relatedCause(code string) claimsv1.RelatedCause {
	switch code {
	case "AA":
		return claimsv1.RelatedCause_RELATED_CAUSE_AUTO_ACCIDENT
	case "EM":
		return claimsv1.RelatedCause_RELATED_CAUSE_EMPLOYMENT
	case "OA":
		return claimsv1.RelatedCause_RELATED_CAUSE_OTHER_ACCIDENT
	}
	return claimsv1.RelatedCause_RELATED_CAUSE_UNKNOWN
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
	s, negative := strings.CutPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	w, err := strconv.ParseUint(whole, 10, 62)
	if err != nil || len(frac) > 2 {
		return 0, false
	}
	f := uint64(0)
	if frac != "" {
		if f, err = strconv.ParseUint((frac + "0")[:2], 10, 8); err != nil {
			return 0, false
		}
	}
	cents := int64(w*100 + f)
	if negative {
		cents = -cents
	}
	return cents, true
}
