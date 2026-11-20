package remittance

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	remittancev1 "coreadmin/gen/coreadmin/remittance/v1"
	"coreadmin/internal/x12"
)

// Implementation guide the 835 follows.
const Guide = "005010X221A1"

// Envelope is how an 835 is addressed.
type Envelope struct {
	Sender, Receiver string
	// ISA13, nine digits; also the group control number.
	Control string
	// P for production, T for test.
	Usage string
	Time  time.Time
}

// X12 renders a remittance as an 835 interchange with one transaction
// set.
func X12(r *remittancev1.Remittance, env Envelope) string {
	var w x12.Writer
	usage := env.Usage
	if usage == "" {
		usage = "P"
	}
	group := strings.TrimLeft(env.Control, "0")
	w.ISA(env.Sender, env.Receiver, env.Time.Format("060102"), env.Time.Format("1504"), env.Control, usage)
	w.Seg("GS", "HP", env.Sender, env.Receiver, env.Time.Format("20060102"), env.Time.Format("1504"), group, "X", Guide)
	w.Seg("ST", "835", "0001", Guide)

	p := r.GetPayment()
	payerID := "1" + r.GetPayer().GetTin()
	date := d8(p.GetDate())
	amount := amt(p.GetAmount().GetCents())
	switch p.GetMethod() {
	case remittancev1.PaymentMethod_PAYMENT_METHOD_ACH:
		w.Seg("BPR", "I", amount, "C", "ACH", "CCP",
			"01", p.GetFromAccount().GetRoutingNumber(), "DA", p.GetFromAccount().GetAccountNumber(), payerID, "",
			"01", p.GetToAccount().GetRoutingNumber(), "DA", p.GetToAccount().GetAccountNumber(), date)
	case remittancev1.PaymentMethod_PAYMENT_METHOD_CHECK:
		w.Seg("BPR", "I", amount, "C", "CHK", "", "", "", "", "", "", "", "", "", "", "", date)
	default:
		// Notification only: nothing is paid.
		w.Seg("BPR", "H", "0", "C", "NON", "", "", "", "", "", "", "", "", "", "", "", date)
	}
	trace := p.GetTraceNumber()
	if trace == "" {
		trace = strings.ReplaceAll(r.GetId(), "-", "")
	}
	w.Seg("TRN", "1", trace, payerID)
	w.Seg("DTM", "405", r.GetCreatedTime().AsTime().Format("20060102"))

	// 1000A payer.
	py := r.GetPayer()
	w.Seg("N1", "PR", upper(py.GetName()))
	if a := py.GetAddress(); a != nil {
		w.Seg("N3", upper(strings.Join(a.GetLines(), " ")))
		w.Seg("N4", upper(a.GetCity()), a.GetState(), a.GetPostalCode())
	}
	w.Seg("PER", "BL", upper(py.GetContactName()), "TE", digits(py.GetContactPhone()))

	// 1000B payee.
	pe := r.GetPayee()
	w.Seg("N1", "PE", upper(pe.GetName()), "XX", pe.GetNpi())
	if pe.GetTin() != "" {
		w.Seg("REF", "TJ", pe.GetTin())
	}

	// 2000 / 2100 / 2110.
	w.Seg("LX", "1")
	for _, e := range r.GetClaims() {
		w.Seg("CLP", e.GetPatientControlNumber(), fmt.Sprint(int32(e.GetStatus())),
			amt(e.GetCharge().GetCents()), amt(e.GetPaid().GetCents()), amt(e.GetPatientResponsibility().GetCents()),
			"CI", e.GetClaimNumber(), e.GetFacilityCode(), e.GetFrequencyCode())
		n := e.GetPatientName()
		w.Seg("NM1", "QC", "1", upper(n.GetLast()), upper(n.GetFirst()), upper(n.GetMiddle()), "", "", "MI", e.GetPatientMemberId())
		if rp := e.GetRenderingProvider(); rp != nil {
			w.Seg("NM1", "82", "1", upper(rp.GetName()), "", "", "", "", "XX", rp.GetNpi())
		}
		if d := e.GetReceivedDate(); d != nil {
			w.Seg("DTM", "050", d8(d))
		}
		for _, l := range e.GetLines() {
			svc := []string{svc01(l), amt(l.GetCharge().GetCents()), amt(l.GetPaid().GetCents())}
			if l.GetProcedureCode() != "" {
				svc = append(svc, l.GetRevenueCode())
			} else {
				svc = append(svc, "")
			}
			if u := l.GetUnitsMilli(); u != 0 && u != 1000 {
				svc = append(svc, units(u))
			}
			w.Seg("SVC", svc...)
			if sd := l.GetServiceDates(); sd.GetStart() != nil {
				if sd.GetEnd() == nil || d8(sd.GetStart()) == d8(sd.GetEnd()) {
					w.Seg("DTM", "472", d8(sd.GetStart()))
				} else {
					w.Seg("DTM", "150", d8(sd.GetStart()))
					w.Seg("DTM", "151", d8(sd.GetEnd()))
				}
			}
			for _, seg := range cas(l.GetAdjustments()) {
				w.Seg("CAS", seg...)
			}
			if a := l.GetAllowed(); a != nil {
				w.Seg("AMT", "B6", amt(a.GetCents()))
			}
			for _, rc := range l.GetRemarkCodes() {
				w.Seg("LQ", "HE", rc)
			}
		}
	}
	w.SE("0001")
	w.Seg("GE", "1", group)
	w.Seg("IEA", "1", env.Control)
	return w.String()
}

var groupCodes = map[claimsv1.CasGroup]string{
	claimsv1.CasGroup_CAS_GROUP_CONTRACTUAL:            "CO",
	claimsv1.CasGroup_CAS_GROUP_PATIENT_RESPONSIBILITY: "PR",
	claimsv1.CasGroup_CAS_GROUP_OTHER:                  "OA",
	claimsv1.CasGroup_CAS_GROUP_PAYER_INITIATED:        "PI",
}

// cas groups adjustments into CAS segments: one per group code, in the
// order the groups first appear, at most six reasons each.
func cas(adjs []*claimsv1.CasAdjustment) [][]string {
	var order []claimsv1.CasGroup
	by := map[claimsv1.CasGroup][]*claimsv1.CasAdjustment{}
	for _, a := range adjs {
		if _, ok := by[a.GetGroup()]; !ok {
			order = append(order, a.GetGroup())
		}
		by[a.GetGroup()] = append(by[a.GetGroup()], a)
	}
	var out [][]string
	for _, g := range order {
		list := by[g]
		for len(list) > 0 {
			n := min(6, len(list))
			seg := []string{groupCodes[g]}
			for _, a := range list[:n] {
				seg = append(seg, a.GetReasonCode(), amt(a.GetAmount().GetCents()), "")
			}
			out = append(out, seg)
			list = list[n:]
		}
	}
	return out
}

// svc01 is the procedure as adjudicated: HC with up to four modifiers, or
// NU with the revenue code for an institutional line with no procedure.
func svc01(l *remittancev1.RemittanceLine) string {
	if l.GetProcedureCode() == "" {
		return "NU:" + l.GetRevenueCode()
	}
	parts := append([]string{"HC", l.GetProcedureCode()}, l.GetModifiers()...)
	return strings.Join(parts[:min(len(parts), 6)], ":")
}

// amt formats cents as an X12 decimal: 180, 65.5, -12.34.
func amt(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	s := strconv.FormatInt(cents/100, 10)
	if f := cents % 100; f != 0 {
		s += strings.TrimRight(fmt.Sprintf(".%02d", f), "0")
	}
	return sign + s
}

func units(milli int64) string {
	s := strconv.FormatInt(milli/1000, 10)
	if f := milli % 1000; f != 0 {
		s += strings.TrimRight(fmt.Sprintf(".%03d", f), "0")
	}
	return s
}

func d8(d *commonv1.Date) string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("%04d%02d%02d", d.GetYear(), d.GetMonth(), d.GetDay())
}

func upper(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}
