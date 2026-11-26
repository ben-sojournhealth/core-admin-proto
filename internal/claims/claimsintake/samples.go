package claimsintake

import (
	"fmt"
	"strings"

	"coreadmin/internal/common/x12"
)

// Sender and receiver ids on the sample files.
const (
	SampleSender = "CLEARPATH"
	OurID        = "COREADMIN"
)

// Sample is an example 837P file for the Claims Intake demo. The samples
// are written against the Journey sandbox: its members, providers and the
// claim numbers its workload creates.
type Sample struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	Filename string `json:"filename"`
	Text     string `json:"text"`
}

type prov struct{ last, first, org, npi, tin, taxonomy string }

var (
	chen  = prov{last: "CHEN", first: "MEI", npi: "1234567893", tin: "311234567", taxonomy: "207Q00000X"}
	ortiz = prov{org: "ORTIZ ORTHOPEDICS", npi: "1987654321", tin: "452345678", taxonomy: "207X00000X"}
)

// ssn is sent as REF*SY when set (SSNs starting with 9 are never issued).
type person struct{ last, first, dob, sex, ssn string }

type svc struct {
	proc    string
	dollars int
	dos     string // YYYYMMDD
}

// cob is another payer on a claim, which has already processed it.
type cob struct {
	payer, payerID string
	subscriber     person
	memberID       string
	// The patient's relationship to that policy's subscriber (SBR02).
	relationship    string
	group           string
	paid, remaining int
	processed       string // YYYYMMDD
	claimNumber     string
}

// extras are the situational parts of a claim. The plain samples send
// none, which keeps their segment positions fixed.
type extras struct {
	// Send addresses for the subscriber, patient and payer.
	addresses bool
	// We are the secondary payer (SBR01 = S); see other.
	secondary bool
	pos       string // place of service, 11 when empty
	// More diagnoses after dx.
	moreDx []string
	// Onset and accident dates (YYYYMMDD); an accident also sends the
	// related cause OA.
	onset, accident string
	referring       *prov
	referral        string // REF*9F
	mrn             string // REF*EA
	note            string
	emergency       bool
	other           *cob
}

type clm struct {
	pcn        string
	card       string
	subscriber person
	// patient is set for a dependent sent in the 2000C loop.
	patient *person
	billing prov
	freq    string // 1, 7 or 8
	ref     string // REF*F8
	auth    string
	dx      string
	lines   []svc
	// noCharge leaves CLM02 empty, which the 999 rejects.
	noCharge bool
	extras
}

type txn struct {
	control string
	claims  []clm
	// badCount writes a wrong SE01.
	badCount bool
}

func file(icn, bhtDate, bhtTime string, sets ...txn) string {
	var w x12.Writer
	w.ISA(SampleSender, OurID, bhtDate[2:], bhtTime, icn, "P")
	gs := strings.TrimLeft(icn, "0")
	w.Seg("GS", "HC", SampleSender, OurID, bhtDate, bhtTime, gs, "X", Guide837P)
	for _, t := range sets {
		w.Seg("ST", "837", t.control, Guide837P)
		w.Seg("BHT", "0019", "00", "B"+gs+t.control, bhtDate, bhtTime, "CH")
		w.Seg("NM1", "41", "2", "CLEARPATH EDI", "", "", "", "", "46", SampleSender)
		w.Seg("PER", "IC", "EDI SUPPORT", "TE", "5555550100")
		w.Seg("NM1", "40", "2", "COREADMIN TPA", "", "", "", "", "46", OurID)
		hl := 0
		for _, c := range t.claims {
			hl++
			bp := hl
			w.Seg("HL", fmt.Sprint(bp), "", "20", "1")
			w.Seg("PRV", "BI", "PXC", c.billing.taxonomy)
			if c.billing.org != "" {
				w.Seg("NM1", "85", "2", c.billing.org, "", "", "", "", "XX", c.billing.npi)
			} else {
				w.Seg("NM1", "85", "1", c.billing.last, c.billing.first, "", "", "", "XX", c.billing.npi)
			}
			w.Seg("N3", "100 MAIN ST")
			w.Seg("N4", "SPRINGFIELD", "IL", "627010000")
			w.Seg("REF", "EI", c.billing.tin)
			hl++
			sub := hl
			child, rel := "0", "18"
			if c.patient != nil {
				child, rel = "1", ""
			}
			w.Seg("HL", fmt.Sprint(sub), fmt.Sprint(bp), "22", child)
			seq := "P"
			if c.secondary {
				seq = "S"
			}
			home := func() {
				if a, ok := homes[c.subscriber.last]; ok && c.addresses {
					w.Seg("N3", a[0])
					w.Seg("N4", a[1], a[2], a[3])
				}
			}
			w.Seg("SBR", seq, rel, "GRP0042", "", "", "", "", "", "CI")
			w.Seg("NM1", "IL", "1", c.subscriber.last, c.subscriber.first, "", "", "", "MI", c.card)
			home()
			w.Seg("DMG", "D8", c.subscriber.dob, c.subscriber.sex)
			if c.subscriber.ssn != "" {
				w.Seg("REF", "SY", c.subscriber.ssn)
			}
			w.Seg("NM1", "PR", "2", "ACME HEALTH PLAN", "", "", "", "", "PI", "ACMEHP")
			if c.addresses {
				w.Seg("N3", "1 PLAN PLAZA")
				w.Seg("N4", "CHICAGO", "IL", "606010000")
			}
			if c.patient != nil {
				hl++
				w.Seg("HL", fmt.Sprint(hl), fmt.Sprint(sub), "23", "0")
				w.Seg("PAT", "19")
				w.Seg("NM1", "QC", "1", c.patient.last, c.patient.first)
				home()
				w.Seg("DMG", "D8", c.patient.dob, c.patient.sex)
			}
			total := 0
			for _, l := range c.lines {
				total += l.dollars
			}
			charge := fmt.Sprint(total)
			if c.noCharge {
				charge = ""
			}
			pos, cause := "11", ""
			if c.pos != "" {
				pos = c.pos
			}
			if c.accident != "" {
				cause = "OA"
			}
			w.Seg("CLM", c.pcn, charge, "", "", pos+":B:"+c.freq, "Y", "A", "Y", "Y", "", cause)
			if c.onset != "" {
				w.Seg("DTP", "431", "D8", c.onset)
			}
			if c.accident != "" {
				w.Seg("DTP", "439", "D8", c.accident)
			}
			if c.ref != "" {
				w.Seg("REF", "F8", c.ref)
			}
			if c.referral != "" {
				w.Seg("REF", "9F", c.referral)
			}
			if c.auth != "" {
				w.Seg("REF", "G1", c.auth)
			}
			w.Seg("REF", "D9", "CP"+gs+c.pcn)
			if c.mrn != "" {
				w.Seg("REF", "EA", c.mrn)
			}
			if c.note != "" {
				w.Seg("NTE", "ADD", c.note)
			}
			hi := []string{"ABK:" + c.dx}
			for _, d := range c.moreDx {
				hi = append(hi, "ABF:"+d)
			}
			w.Seg("HI", hi...)
			if r := c.referring; r != nil {
				w.Seg("NM1", "DN", "1", r.last, r.first, "", "", "", "XX", r.npi)
			}
			if c.billing.org == "" {
				w.Seg("NM1", "82", "1", c.billing.last, c.billing.first, "", "", "", "XX", c.billing.npi)
				w.Seg("PRV", "PE", "PXC", c.billing.taxonomy)
			}
			if o := c.other; o != nil {
				w.Seg("SBR", "P", o.relationship, o.group, "", "", "", "", "", "CI")
				w.Seg("AMT", "D", fmt.Sprint(o.paid))
				w.Seg("AMT", "EAF", fmt.Sprint(o.remaining))
				w.Seg("OI", "", "", "Y", "P", "", "Y")
				w.Seg("NM1", "IL", "1", o.subscriber.last, o.subscriber.first, "", "", "", "MI", o.memberID)
				w.Seg("NM1", "PR", "2", o.payer, "", "", "", "", "PI", o.payerID)
				w.Seg("DTP", "573", "D8", o.processed)
				w.Seg("REF", "F8", o.claimNumber)
			}
			emergency := ""
			if c.emergency {
				emergency = "Y"
			}
			pointers := "1"
			for i := range c.moreDx {
				pointers += fmt.Sprintf(":%d", i+2)
			}
			for i, l := range c.lines {
				w.Seg("LX", fmt.Sprint(i+1))
				w.Seg("SV1", "HC:"+l.proc, fmt.Sprint(l.dollars), "UN", "1", "", "", pointers, "", emergency)
				w.Seg("DTP", "472", "D8", l.dos)
			}
		}
		if t.badCount {
			w.Seg("SE", "7", t.control)
		} else {
			w.SE(t.control)
		}
	}
	w.Seg("GE", fmt.Sprint(len(sets)), gs)
	w.Seg("IEA", "1", icn)
	return w.String()
}

var (
	jordan = person{"RIVERA", "JORDAN", "19850412", "U", "999101001"}
	sam    = person{"RIVERA", "SAM", "19870903", "U", ""}
	riley  = person{"RIVERA", "RILEY", "20150621", "U", ""}
	priya  = person{"PATEL", "PRIYA", "19791102", "F", ""}
	arjun  = person{"PATEL", "ARJUN", "19770519", "M", ""}
	chidi  = person{"OKAFOR", "CHIDI", "19900208", "M", ""}
	ada    = person{"OKAFOR", "ADA", "20190830", "F", ""}
	taylor = person{"NGUYEN", "TAYLOR", "19920314", "U", ""}
	avery  = person{"RIVERA", "AVERY", "20260302", "U", ""}
	lucia  = person{"MORALES", "LUCIA", "19680714", "F", ""}

	okonkwo = prov{last: "OKONKWO", first: "NGOZI", npi: "1122334455"}
)

// homes are the families' addresses, by last name: street, city, state,
// ZIP.
var homes = map[string][4]string{
	"RIVERA":  {"42 ELM ST", "SPRINGFIELD", "IL", "627040000"},
	"PATEL":   {"1180 ORCHARD LN", "SPRINGFIELD", "IL", "627110000"},
	"OKAFOR":  {"7 QUARRY RD APT 3B", "CHATHAM", "IL", "626290000"},
	"MORALES": {"315 N 9TH ST", "SPRINGFIELD", "IL", "627020000"},
}

// Samples is the demo's file library.
func Samples() []Sample {
	return []Sample{
		{
			ID: "clean", Title: "Clean batch", Filename: "CLEARPATH_000000101.837",
			Summary: "Three claims in one transaction set, one with two lines.",
			Text: file("000000101", "20260315", "2210", txn{control: "0001", claims: []clm{
				{pcn: "RIV-88213", card: "ACM100101", subscriber: jordan, billing: chen, freq: "1", dx: "J069",
					lines: []svc{{"99213", 180, "20260312"}}},
				{pcn: "PAT-40711", card: "ACM200101", subscriber: priya, billing: chen, freq: "1", dx: "E119",
					lines: []svc{{"99214", 250, "20260311"}}},
				{pcn: "OKA-10552", card: "ACM300101", subscriber: chidi, billing: chen, freq: "1", dx: "M545",
					lines: []svc{{"99214", 250, "20260312"}, {"99213", 180, "20260313"}}},
			}}),
		},
		{
			ID: "dependent", Title: "Dependent in the patient loop", Filename: "CLEARPATH_000000102.837",
			Summary: "Riley sent in the 2000C patient loop under Jordan's member id.",
			Text: file("000000102", "20260315", "2215", txn{control: "0001", claims: []clm{
				{pcn: "RIV-88240", card: "ACM100101", subscriber: jordan, patient: &riley, billing: chen, freq: "1", dx: "J029",
					lines: []svc{{"99214", 250, "20260313"}}},
			}}),
		},
		{
			ID: "rich", Title: "A claim with everything on it", Filename: "CLEARPATH_000000110.837",
			Summary: "An accident claim for a dependent: other insurance that already paid, a referral, an attachment, notes and a drug line.",
			Text:    richFile(),
		},
		{
			ID: "unknown-member", Title: "Unknown member", Filename: "CLEARPATH_000000103.837",
			Summary: "A member id that isn't in the sandbox's enrollment.",
			Text: file("000000103", "20260315", "2220", txn{control: "0001", claims: []clm{
				{pcn: "NGU-30017", card: "ACM999901", subscriber: taylor, billing: chen, freq: "1", dx: "R51",
					lines: []svc{{"99213", 180, "20260313"}}},
			}}),
		},
		{
			ID: "corrected", Title: "Corrected claim, quoting an old version", Filename: "CLEARPATH_000000104.837",
			Summary: "Frequency 7, quoting 26063-E-00001 (that claim is already at version -2).",
			Text: file("000000104", "20260316", "0610", txn{control: "0001", claims: []clm{
				{pcn: "RIV-87990", card: "ACM100101", subscriber: jordan, billing: chen, freq: "7", ref: "26063-E-00001", dx: "M2351",
					lines: []svc{{"99214", 250, "20260303"}}},
			}}),
		},
		{
			ID: "corrected-no-match", Title: "Corrected claim, no match", Filename: "CLEARPATH_000000105.837",
			Summary: "Frequency 7, quoting a claim number that was never issued.",
			Text: file("000000105", "20260316", "0615", txn{control: "0001", claims: []clm{
				{pcn: "PAT-39002", card: "ACM200102", subscriber: arjun, billing: chen, freq: "7", ref: "26040-E-00417", dx: "M6281",
					lines: []svc{{"99213", 180, "20260209"}}},
			}}),
		},
		{
			ID: "corrected-pended", Title: "Corrected claim for a pended claim", Filename: "CLEARPATH_000000106.837",
			Summary: "Frequency 7 for 26068-E-00001 (still pended), adding an auth number.",
			Text: file("000000106", "20260316", "0620", txn{control: "0001", claims: []clm{
				{pcn: "RIV-88102", card: "ACM100102", subscriber: sam, billing: chen, freq: "7", ref: "26068-E-00001", auth: "PA-1002", dx: "G4489",
					lines: []svc{{"70551", 900, "20260306"}}},
			}}),
		},
		{
			ID: "void", Title: "Void of a paid claim", Filename: "CLEARPATH_000000107.837",
			Summary: "Frequency 8 for 26064-E-00001, a paid claim.",
			Text: file("000000107", "20260316", "0625", txn{control: "0001", claims: []clm{
				{pcn: "PAT-40388", card: "ACM200101", subscriber: priya, billing: chen, freq: "8", ref: "26064-E-00001", dx: "E119",
					lines: []svc{{"99214", 250, "20260304"}}},
			}}),
		},
		{
			ID: "bad-set", Title: "One bad transaction set", Filename: "CLEARPATH_000000108.837",
			Summary: "Two sets; the second is missing a total charge. The 999 rejects only that set.",
			Text: file("000000108", "20260316", "0630",
				txn{control: "0001", claims: []clm{
					{pcn: "PAT-40790", card: "ACM200102", subscriber: arjun, billing: chen, freq: "1", dx: "M545",
						lines: []svc{{"99214", 250, "20260312"}}},
				}},
				txn{control: "0002", claims: []clm{
					{pcn: "OKA-10601", card: "ACM300102", subscriber: ada, billing: chen, freq: "1", dx: "J069", noCharge: true,
						lines: []svc{{"99213", 180, "20260313"}}},
				}},
			),
		},
		{
			ID: "bad-envelope", Title: "Wrong segment count", Filename: "CLEARPATH_000000109.837",
			Summary: "SE01 doesn't match the segments sent. The set is rejected.",
			Text: file("000000109", "20260316", "0635", txn{control: "0001", badCount: true, claims: []clm{
				{pcn: "OKA-10610", card: "ACM300101", subscriber: chidi, billing: ortiz, freq: "1", dx: "M2351",
					lines: []svc{{"29881", 2500, "20260314"}}},
			}}),
		},
	}
}

// richFile is one claim that uses far more of the 837P than the others:
// addresses and contacts, a pay-to address, the payer, an accident, claim
// dates, an attachment, notes, referring and service facility providers,
// another payer that has already paid (with its line adjudication), and a
// drug line with an NDC.
func richFile() string {
	const icn, date, clock, gs, control = "000000110", "20260316", "0640", "110", "0001"
	var w x12.Writer
	w.ISA(SampleSender, OurID, date[2:], clock, icn, "P")
	w.Seg("GS", "HC", SampleSender, OurID, date, clock, gs, "X", Guide837P)
	w.Seg("ST", "837", control, Guide837P)
	w.Seg("BHT", "0019", "00", "B"+gs+control, date, clock, "CH")
	w.Seg("NM1", "41", "2", "CLEARPATH EDI", "", "", "", "", "46", SampleSender)
	w.Seg("PER", "IC", "EDI SUPPORT", "TE", "5555550100")
	w.Seg("NM1", "40", "2", "COREADMIN TPA", "", "", "", "", "46", OurID)

	// Billing provider, with a separate pay-to address.
	w.Seg("HL", "1", "", "20", "1")
	w.Seg("PRV", "BI", "PXC", chen.taxonomy)
	w.Seg("NM1", "85", "1", chen.last, chen.first, "", "", "", "XX", chen.npi)
	w.Seg("N3", "100 MAIN ST", "SUITE 200")
	w.Seg("N4", "SPRINGFIELD", "IL", "627010000")
	w.Seg("REF", "EI", chen.tin)
	w.Seg("REF", "0B", "IL036123456")
	w.Seg("PER", "IC", "BILLING OFFICE", "TE", "5555550111", "EM", "BILLING@CHENFM.EXAMPLE")
	w.Seg("NM1", "87", "2")
	w.Seg("N3", "PO BOX 4410")
	w.Seg("N4", "SPRINGFIELD", "IL", "627050000")

	// Subscriber and payer.
	w.Seg("HL", "2", "1", "22", "1")
	w.Seg("SBR", "S", "", "GRP0042", "", "", "", "", "", "CI")
	w.Seg("NM1", "IL", "1", jordan.last, jordan.first, "A", "", "", "MI", "ACM100101")
	w.Seg("N3", "42 ELM ST")
	w.Seg("N4", "SPRINGFIELD", "IL", "627040000")
	w.Seg("DMG", "D8", jordan.dob, jordan.sex)
	w.Seg("REF", "SY", jordan.ssn)
	w.Seg("NM1", "PR", "2", "ACME HEALTH PLAN", "", "", "", "", "PI", "ACMEHP")
	w.Seg("N3", "1 PLAN PLAZA")
	w.Seg("N4", "CHICAGO", "IL", "606010000")
	w.Seg("REF", "G2", "CHEN0042")

	// Patient: Jordan's child.
	w.Seg("HL", "3", "2", "23", "0")
	w.Seg("PAT", "19")
	w.Seg("NM1", "QC", "1", riley.last, riley.first)
	w.Seg("N3", "42 ELM ST")
	w.Seg("N4", "SPRINGFIELD", "IL", "627040000")
	w.Seg("DMG", "D8", riley.dob, riley.sex)

	// The claim: an accident, with a referral, an attachment and a note.
	w.Seg("CLM", "RIV-88301", "405", "", "", "11:B:1", "Y", "A", "Y", "Y", "", "OA")
	w.Seg("DTP", "431", "D8", "20260310")
	w.Seg("DTP", "439", "D8", "20260310")
	w.Seg("DTP", "304", "D8", "20260312")
	w.Seg("PWK", "OZ", "BM", "", "", "AC", "ATT88301")
	w.Seg("AMT", "F5", "30")
	w.Seg("REF", "9F", "REF-2207")
	w.Seg("REF", "D9", "CP"+gs+"RIV-88301")
	w.Seg("REF", "EA", "MRN-77120")
	w.Seg("NTE", "ADD", "FELL FROM PLAYGROUND EQUIPMENT AT SCHOOL")
	w.Seg("HI", "ABK:S52501A", "ABF:W091XXA")
	w.Seg("NM1", "DN", "1", "OKONKWO", "NGOZI", "", "", "", "XX", "1122334455")
	w.Seg("NM1", "82", "1", chen.last, chen.first, "", "", "", "XX", chen.npi)
	w.Seg("PRV", "PE", "PXC", chen.taxonomy)
	w.Seg("NM1", "77", "2", "SPRINGFIELD URGENT CARE", "", "", "", "", "XX", "1555666777")
	w.Seg("N3", "900 LAKE AVE")
	w.Seg("N4", "SPRINGFIELD", "IL", "627020000")

	// The other parent's plan, which is primary and has already paid.
	w.Seg("SBR", "P", "19", "GRP7781", "", "", "", "", "", "CI")
	w.Seg("AMT", "D", "180")
	w.Seg("AMT", "EAF", "70")
	w.Seg("OI", "", "", "Y", "P", "", "Y")
	w.Seg("NM1", "IL", "1", sam.last, sam.first, "", "", "", "MI", "BLU5550099")
	w.Seg("N3", "42 ELM ST")
	w.Seg("N4", "SPRINGFIELD", "IL", "627040000")
	w.Seg("NM1", "PR", "2", "BLUE RIVER HEALTH", "", "", "", "", "PI", "BRH01")
	w.Seg("DTP", "573", "D8", "20260315")
	w.Seg("REF", "F8", "BRH-2026-118840")

	// An office visit.
	w.Seg("LX", "1")
	w.Seg("SV1", "HC:99214:25", "250", "UN", "1", "11", "", "1:2")
	w.Seg("DTP", "472", "D8", "20260314")
	w.Seg("REF", "6R", "L1-88301")
	w.Seg("NTE", "ADD", "SEEN FOR FOREARM INJURY AFTER FALL")
	w.Seg("SVD", "BRH01", "120", "HC:99214:25", "", "1")
	w.Seg("CAS", "CO", "45", "80")
	w.Seg("CAS", "PR", "1", "50")
	w.Seg("DTP", "573", "D8", "20260315")
	w.Seg("AMT", "EAF", "50")

	// An injection, with its drug.
	w.Seg("LX", "2")
	w.Seg("SV1", "HC:J1885", "155", "UN", "2", "11", "", "1")
	w.Seg("DTP", "472", "D8", "20260314")
	w.Seg("REF", "6R", "L2-88301")
	w.Seg("LIN", "", "N4", "00409379501")
	w.Seg("CTP", "", "", "", "2", "ML")
	w.Seg("REF", "XZ", "RX4471")
	w.Seg("NM1", "DK", "1", chen.last, chen.first, "", "", "", "XX", chen.npi)
	w.Seg("SVD", "BRH01", "60", "HC:J1885", "", "2")
	w.Seg("CAS", "CO", "45", "75")
	w.Seg("CAS", "PR", "2", "20")
	w.Seg("DTP", "573", "D8", "20260315")
	w.Seg("AMT", "EAF", "20")

	w.SE(control)
	w.Seg("GE", "1", gs)
	w.Seg("IEA", "1", icn)
	return w.String()
}
