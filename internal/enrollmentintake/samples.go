package enrollmentintake

import (
	"fmt"
	"strings"

	"coreadmin/internal/x12"
)

// Senders and receiver on the sample files: each client's HR system, and
// one COBRA vendor serving both clients. We receive.
const (
	SampleSender = "ACMEHR"
	BrightSender = "BRIGHTHR"
	COBRAVendor  = "COBRAVND"
	OurID        = "COREADMIN"
)

// sponsor is the plan sponsor named in N1*P5.
type sponsor struct{ name, fein string }

var (
	acme       = sponsor{"ACME MANUFACTURING", "831234567"}
	brightline = sponsor{"BRIGHTLINE LOGISTICS", "862345678"}
)

// Sample is an example 834 file for the Enrollment Intake demo. Existing
// people are the families in the Journey sandbox; member ids are fake
// (SSNs starting with 9 are never issued).
type Sample struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	Filename string `json:"filename"`
	Text     string `json:"text"`
}

// member is one INS loop to write.
type member struct {
	sub          bool
	rel          string // INS02
	maint        string // INS03
	reason       string // INS04
	status       string // INS05, default A
	cobraEvent   string // INS07
	employment   string // INS08
	subNumber    string // REF*0F
	last, first  string
	ssn          string
	dob, sex     string
	street, city string
	phone        string
	dates        [][2]string // member DTPs: qualifier, date
	cov          *coverage
}

type coverage struct {
	maint, level string
	dates        [][2]string
}

type txn struct {
	control string
	// N1*P5; Acme when empty.
	sponsor sponsor
	action  string // BGN08
	// DTP*007, the file's effective date; the file date when empty.
	effective string
	members   []member
	// badCount writes a wrong SE01.
	badCount bool
}

func file(icn, date, tm string, sets ...txn) string {
	return fileFrom(SampleSender, icn, date, tm, sets...)
}

func fileFrom(sender, icn, date, tm string, sets ...txn) string {
	var w x12.Writer
	w.ISA(sender, OurID, date[2:], tm, icn, "P")
	gs := strings.TrimLeft(icn, "0")
	w.Seg("GS", "BE", sender, OurID, date, tm, gs, "X", Guide834)
	for _, t := range sets {
		w.Seg("ST", "834", t.control, Guide834)
		w.Seg("BGN", "00", "ACME"+gs+t.control, date, tm, "", "", "", t.action)
		w.Seg("REF", "38", "ACME-PPO")
		eff := t.effective
		if eff == "" {
			eff = date
		}
		w.Seg("DTP", "007", "D8", eff)
		sp := t.sponsor
		if sp.name == "" {
			sp = acme
		}
		w.Seg("N1", "P5", sp.name, "FI", sp.fein)
		w.Seg("N1", "IN", "COREADMIN TPA", "FI", "870000001")
		if sender == COBRAVendor {
			w.Seg("N1", "TV", "COBRA VENDOR INC", "FI", "880000001")
		}
		for _, m := range t.members {
			status := m.status
			if status == "" {
				status = "A"
			}
			sub := map[bool]string{true: "Y", false: "N"}[m.sub]
			w.Seg("INS", sub, m.rel, m.maint, m.reason, status, "", m.cobraEvent, m.employment)
			w.Seg("REF", "0F", m.subNumber)
			if m.sub {
				w.Seg("REF", "1L", "ACME-PPO-01")
			}
			for _, d := range m.dates {
				w.Seg("DTP", d[0], "D8", d[1])
			}
			w.Seg("NM1", "IL", "1", m.last, m.first, "", "", "", "34", m.ssn)
			if m.phone != "" {
				w.Seg("PER", "IP", "", "TE", m.phone)
			}
			if m.street != "" {
				w.Seg("N3", m.street)
				w.Seg("N4", m.city, "OH", "45402")
			}
			if m.dob != "" {
				w.Seg("DMG", "D8", m.dob, m.sex)
			}
			if c := m.cov; c != nil {
				w.Seg("HD", c.maint, "", "HLT", "PPO", c.level)
				for _, d := range c.dates {
					w.Seg("DTP", d[0], "D8", d[1])
				}
			}
		}
		if t.badCount {
			w.Seg("SE", "4", t.control)
		} else {
			w.SE(t.control)
		}
	}
	w.Seg("GE", fmt.Sprint(len(sets)), gs)
	w.Seg("IEA", "1", icn)
	return w.String()
}

// The Rivera, Patel and Okafor families are enrolled by the baseline
// file; the Nguyens are new.
var (
	jordan = member{sub: true, rel: "18", subNumber: "E10101", last: "RIVERA", first: "JORDAN", ssn: "999101001", dob: "19850412", sex: "M"}
	alex   = member{rel: "01", subNumber: "E10101", last: "RIVERA", first: "ALEX", ssn: "999101002", dob: "19860719", sex: "F"}
	riley  = member{rel: "19", subNumber: "E10101", last: "RIVERA", first: "RILEY", ssn: "999101003", dob: "20150521", sex: "F"}
	priya  = member{sub: true, rel: "18", subNumber: "E10201", last: "PATEL", first: "PRIYA", ssn: "999102001", dob: "19880903", sex: "F"}
	arjun  = member{rel: "01", subNumber: "E10201", last: "PATEL", first: "ARJUN", ssn: "999102002", dob: "19870224", sex: "M"}
	chidi  = member{sub: true, rel: "18", subNumber: "E10301", last: "OKAFOR", first: "CHIDI", ssn: "999103001", dob: "19790117", sex: "M"}

	// Brightline's people.
	diego = member{sub: true, rel: "18", subNumber: "B20101", last: "MORALES", first: "DIEGO", ssn: "999201001", dob: "19820605", sex: "M"}
	ana   = member{rel: "01", subNumber: "B20101", last: "MORALES", first: "ANA", ssn: "999201002", dob: "19840911", sex: "F"}
	kim   = member{sub: true, rel: "18", subNumber: "B20201", last: "LEE", first: "KIM", ssn: "999202001", dob: "19910128", sex: "F"}
)

// Baselines are each client's open-enrollment full file, enrolling
// everyone from January 1. A sandbox loads them first, so the samples have
// people to change.
func Baselines() []Sample {
	enrolled := func(m member, level string) member {
		m.maint = "030"
		if m.sub {
			m.employment = "FT"
		}
		m.dates = [][2]string{{"356", "20260101"}}
		m.cov = &coverage{maint: "030", level: level, dates: [][2]string{{"348", "20260101"}}}
		return m
	}
	return []Sample{
		{ID: "baseline-acme", Title: "Acme open enrollment", Summary: "Full file enrolling the Riveras, Patels and Okafors from January 1.",
			Filename: "ACMEHR_000000200.834",
			Text: file("000000200", "20251215", "0600", txn{control: "0001", action: "RX", effective: "20260101", members: []member{
				enrolled(jordan, "FAM"), enrolled(alex, "FAM"), enrolled(riley, "FAM"),
				enrolled(priya, "ESP"), enrolled(arjun, "ESP"),
				enrolled(chidi, "EMP"),
			}})},
		{ID: "baseline-brightline", Title: "Brightline open enrollment", Summary: "Full file enrolling the Moraleses and Kim Lee from January 1.",
			Filename: "BRIGHTHR_000000400.834",
			Text: fileFrom(BrightSender, "000000400", "20251218", "0600", txn{control: "0001", sponsor: brightline, action: "RX", effective: "20260101", members: []member{
				enrolled(diego, "ESP"), enrolled(ana, "ESP"), enrolled(kim, "EMP"),
			}})},
	}
}

func with(m member, f func(*member)) member { f(&m); return m }

// Samples lists the demo files. Interchange control numbers are unique, so
// each can be loaded once.
func Samples() []Sample {
	return []Sample{
		{ID: "new-hire", Title: "New hire with family",
			Summary:  "Subscriber, spouse and child added together, family coverage from April 1.",
			Filename: "ACMEHR_000000201.834",
			Text: file("000000201", "20260316", "0715", txn{control: "0001", action: "2", members: []member{
				{sub: true, rel: "18", maint: "021", reason: "28", employment: "FT", subNumber: "E10501", last: "NGUYEN", first: "SAM", ssn: "999105001",
					dob: "19900214", sex: "M", street: "42 ELM ST", city: "DAYTON", phone: "9375550111",
					dates: [][2]string{{"336", "20260316"}, {"356", "20260401"}},
					cov:   &coverage{maint: "021", level: "FAM", dates: [][2]string{{"348", "20260401"}}}},
				{rel: "01", maint: "021", reason: "28", subNumber: "E10501", last: "NGUYEN", first: "LINH", ssn: "999105002", dob: "19910630", sex: "F",
					dates: [][2]string{{"356", "20260401"}}, cov: &coverage{maint: "021", level: "FAM", dates: [][2]string{{"348", "20260401"}}}},
				{rel: "19", maint: "021", reason: "28", subNumber: "E10501", last: "NGUYEN", first: "AN", ssn: "999105003", dob: "20190811", sex: "M",
					dates: [][2]string{{"356", "20260401"}}, cov: &coverage{maint: "021", level: "FAM", dates: [][2]string{{"348", "20260401"}}}},
			}})},
		{ID: "newborn", Title: "Newborn added",
			Summary:  "A dependent added for a birth (reason 02), effective the date of birth. Only the baby's loop is sent.",
			Filename: "ACMEHR_000000202.834",
			Text: file("000000202", "20260316", "0716", txn{control: "0001", action: "2", members: []member{
				{rel: "19", maint: "021", reason: "02", subNumber: "E10101", last: "RIVERA", first: "NOAH", ssn: "999101004", dob: "20260302", sex: "M",
					dates: [][2]string{{"356", "20260302"}}, cov: &coverage{maint: "021", level: "FAM", dates: [][2]string{{"348", "20260302"}}}},
			}})},
		{ID: "termination", Title: "Termination",
			Summary:  "Subscriber terminated with employment (reason 08) at the end of March. Dependents aren't sent.",
			Filename: "ACMEHR_000000203.834",
			Text: file("000000203", "20260316", "0717", txn{control: "0001", action: "2", members: []member{
				with(priya, func(m *member) {
					m.maint, m.reason, m.employment = "024", "08", "TE"
					m.dates = [][2]string{{"337", "20260331"}, {"357", "20260331"}}
					m.cov = &coverage{maint: "024", level: "FAM", dates: [][2]string{{"349", "20260331"}}}
				}),
			}})},
		{ID: "retro-term", Title: "Retroactive termination",
			Summary:  "Sent March 16, ending coverage January 31: claims paid since then may need recovery.",
			Filename: "ACMEHR_000000204.834",
			Text: file("000000204", "20260316", "0718", txn{control: "0001", action: "2", members: []member{
				with(chidi, func(m *member) {
					m.maint, m.reason, m.employment = "024", "07", "TE"
					m.dates = [][2]string{{"357", "20260131"}}
					m.cov = &coverage{maint: "024", level: "EMP", dates: [][2]string{{"349", "20260131"}}}
				}),
			}})},
		{ID: "reinstatement", Title: "Reinstatement",
			Summary:  "Chidi is reinstated (025) from February 1, undoing the retroactive termination.",
			Filename: "ACMEHR_000000211.834",
			Text: file("000000211", "20260317", "0715", txn{control: "0001", action: "2", members: []member{
				with(chidi, func(m *member) {
					m.maint, m.employment = "025", "FT"
					m.dates = [][2]string{{"356", "20260201"}}
					m.cov = &coverage{maint: "025", level: "EMP", dates: [][2]string{{"348", "20260201"}}}
				}),
			}})},
		{ID: "address-change", Title: "Address change",
			Summary:  "A change (001) with a new address and no coverage loop: coverage isn't touched.",
			Filename: "ACMEHR_000000205.834",
			Text: file("000000205", "20260316", "0719", txn{control: "0001", action: "2", members: []member{
				with(jordan, func(m *member) {
					m.maint, m.employment = "001", "FT"
					m.street, m.city, m.phone = "1150 RIVERSIDE DR", "DAYTON", "9375550142"
					m.dates = [][2]string{{"303", "20260315"}}
				}),
			}})},
		{ID: "brightline-term", Title: "Brightline termination",
			Summary:  "Brightline's HR system ends Kim Lee's coverage with her employment at the end of March.",
			Filename: "BRIGHTHR_000000401.834",
			Text: fileFrom(BrightSender, "000000401", "20260316", "0730", txn{control: "0001", sponsor: brightline, action: "2", members: []member{
				with(kim, func(m *member) {
					m.maint, m.reason, m.employment = "024", "08", "TE"
					m.dates = [][2]string{{"357", "20260331"}}
					m.cov = &coverage{maint: "024", level: "EMP", dates: [][2]string{{"349", "20260331"}}}
				}),
			}})},
		{ID: "cobra", Title: "COBRA elections from the vendor",
			Summary:  "One file from the COBRA vendor for two clients: Priya Patel (Acme) and Kim Lee (Brightline) continue on COBRA from April 1.",
			Filename: "COBRAVND_000000301.834",
			Text: fileFrom(COBRAVendor, "000000301", "20260316", "0800",
				txn{control: "0001", sponsor: acme, action: "2", members: []member{
					with(priya, func(m *member) {
						m.maint, m.status, m.cobraEvent, m.employment = "021", "C", "1", "TE"
						m.dates = [][2]string{{"340", "20260401"}, {"356", "20260401"}}
						m.cov = &coverage{maint: "021", level: "EMP", dates: [][2]string{{"348", "20260401"}}}
					}),
				}},
				txn{control: "0002", sponsor: brightline, action: "2", members: []member{
					with(kim, func(m *member) {
						m.maint, m.status, m.cobraEvent, m.employment = "021", "C", "1", "TE"
						m.dates = [][2]string{{"340", "20260401"}, {"356", "20260401"}}
						m.cov = &coverage{maint: "021", level: "EMP", dates: [][2]string{{"348", "20260401"}}}
					}),
				}})},
		{ID: "cobra-full-file", Title: "COBRA vendor full file",
			Summary:  "The vendor's full file of Acme continuees, effective May 1. It speaks only for COBRA members, so Acme's employees aren't touched.",
			Filename: "COBRAVND_000000302.834",
			Text: fileFrom(COBRAVendor, "000000302", "20260501", "0600", txn{control: "0001", sponsor: acme, action: "RX", members: []member{
				with(priya, func(m *member) {
					m.maint, m.status, m.cobraEvent, m.employment = "030", "C", "1", "TE"
					m.dates = [][2]string{{"340", "20260401"}}
					m.cov = &coverage{maint: "030", level: "EMP", dates: [][2]string{{"348", "20260401"}}}
				}),
			}})},
		{ID: "cobra-from-employer", Title: "COBRA from the employer",
			Summary:  "Acme's HR system sends a COBRA enrollment, but Acme's COBRA is run by the vendor.",
			Filename: "ACMEHR_000000206.834",
			Text: file("000000206", "20260316", "0720", txn{control: "0001", action: "2", members: []member{
				with(priya, func(m *member) {
					m.maint, m.status, m.cobraEvent, m.employment = "021", "C", "1", "TE"
					m.dates = [][2]string{{"340", "20260401"}, {"356", "20260401"}}
					m.cov = &coverage{maint: "021", level: "EMP", dates: [][2]string{{"348", "20260401"}}}
				}),
			}})},
		{ID: "full-file", Title: "Full file",
			Summary:  "BGN08 RX, effective April 1: every enrolled member, each as an audit (030). The Patels aren't in it.",
			Filename: "ACMEHR_000000207.834",
			Text: file("000000207", "20260401", "0600", txn{control: "0001", action: "RX", members: []member{
				with(jordan, func(m *member) {
					m.maint, m.employment = "030", "FT"
					m.cov = &coverage{maint: "030", level: "FAM", dates: [][2]string{{"348", "20260101"}}}
				}),
				with(alex, func(m *member) {
					m.maint = "030"
					m.cov = &coverage{maint: "030", level: "FAM", dates: [][2]string{{"348", "20260101"}}}
				}),
				with(riley, func(m *member) {
					m.maint = "030"
					m.cov = &coverage{maint: "030", level: "FAM", dates: [][2]string{{"348", "20260101"}}}
				}),
				with(chidi, func(m *member) {
					m.maint, m.employment = "030", "FT"
					m.cov = &coverage{maint: "030", level: "EMP", dates: [][2]string{{"348", "20260101"}}}
				}),
			}})},
		{ID: "bad-set", Title: "One bad transaction set",
			Summary:  "Two sets; the second has an eligibility date of 2026-13-40, which isn't a date. The 999 rejects only that set.",
			Filename: "ACMEHR_000000208.834",
			Text: file("000000208", "20260316", "0721",
				txn{control: "0001", action: "2", members: []member{
					with(riley, func(m *member) { m.maint, m.street, m.city = "001", "1150 RIVERSIDE DR", "DAYTON" }),
				}},
				txn{control: "0002", action: "2", members: []member{
					with(chidi, func(m *member) { m.maint, m.dates = "001", [][2]string{{"356", "20261340"}} }),
				}})},
		{ID: "unknown-code", Title: "Unrecognized maintenance type",
			Summary:  "INS03 is 099, which isn't a code we know. The 999 accepts it; it arrives as UNKNOWN with the code kept as sent.",
			Filename: "ACMEHR_000000210.834",
			Text: file("000000210", "20260316", "0723", txn{control: "0001", action: "2", members: []member{
				with(chidi, func(m *member) { m.maint, m.phone = "099", "9375550177" }),
			}})},
		{ID: "bad-envelope", Title: "Wrong segment count",
			Summary:  "SE01 doesn't match the segments sent. The set is rejected.",
			Filename: "ACMEHR_000000209.834",
			Text: file("000000209", "20260316", "0722", txn{control: "0001", action: "2", badCount: true, members: []member{
				with(jordan, func(m *member) { m.maint, m.phone = "001", "9375550199" }),
			}})},
	}
}
