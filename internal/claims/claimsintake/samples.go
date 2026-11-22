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

type person struct{ last, first, dob, sex string }

type svc struct {
	proc    string
	dollars int
	dos     string // YYYYMMDD
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
			w.Seg("SBR", "P", rel, "GRP0042", "", "", "", "", "", "CI")
			w.Seg("NM1", "IL", "1", c.subscriber.last, c.subscriber.first, "", "", "", "MI", c.card)
			w.Seg("DMG", "D8", c.subscriber.dob, c.subscriber.sex)
			w.Seg("NM1", "PR", "2", "ACME HEALTH PLAN", "", "", "", "", "PI", "ACMEHP")
			if c.patient != nil {
				hl++
				w.Seg("HL", fmt.Sprint(hl), fmt.Sprint(sub), "23", "0")
				w.Seg("PAT", "19")
				w.Seg("NM1", "QC", "1", c.patient.last, c.patient.first)
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
			w.Seg("CLM", c.pcn, charge, "", "", "11:B:"+c.freq, "Y", "A", "Y", "Y")
			if c.ref != "" {
				w.Seg("REF", "F8", c.ref)
			}
			if c.auth != "" {
				w.Seg("REF", "G1", c.auth)
			}
			w.Seg("REF", "D9", "CP"+gs+c.pcn)
			w.Seg("HI", "ABK:"+c.dx)
			if c.billing.org == "" {
				w.Seg("NM1", "82", "1", c.billing.last, c.billing.first, "", "", "", "XX", c.billing.npi)
				w.Seg("PRV", "PE", "PXC", c.billing.taxonomy)
			}
			for i, l := range c.lines {
				w.Seg("LX", fmt.Sprint(i+1))
				w.Seg("SV1", "HC:"+l.proc, fmt.Sprint(l.dollars), "UN", "1", "", "", "1")
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
	jordan = person{"RIVERA", "JORDAN", "19850412", "U"}
	sam    = person{"RIVERA", "SAM", "19870903", "U"}
	riley  = person{"RIVERA", "RILEY", "20150621", "U"}
	priya  = person{"PATEL", "PRIYA", "19791102", "F"}
	arjun  = person{"PATEL", "ARJUN", "19770519", "M"}
	chidi  = person{"OKAFOR", "CHIDI", "19900208", "M"}
	ada    = person{"OKAFOR", "ADA", "20190830", "F"}
	taylor = person{"NGUYEN", "TAYLOR", "19920314", "U"}
)

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
			ID: "unknown-member", Title: "Unknown member", Filename: "CLEARPATH_000000103.837",
			Summary: "A member id that isn't in the sandbox's enrollment.",
			Text: file("000000103", "20260315", "2220", txn{control: "0001", claims: []clm{
				{pcn: "NGU-30017", card: "ACM999901", subscriber: taylor, billing: chen, freq: "1", dx: "R51",
					lines: []svc{{"99213", 180, "20260313"}}},
			}}),
		},
		{
			ID: "corrected", Title: "Corrected claim, quoting an old version", Filename: "CLEARPATH_000000104.837",
			Summary: "Frequency 7, quoting 26063-A-00001 (that claim is already at version -2).",
			Text: file("000000104", "20260316", "0610", txn{control: "0001", claims: []clm{
				{pcn: "RIV-87990", card: "ACM100101", subscriber: jordan, billing: chen, freq: "7", ref: "26063-A-00001", dx: "M2351",
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
			Summary: "Frequency 7 for 26068-A-00001 (still pended), adding an auth number.",
			Text: file("000000106", "20260316", "0620", txn{control: "0001", claims: []clm{
				{pcn: "RIV-88102", card: "ACM100102", subscriber: sam, billing: chen, freq: "7", ref: "26068-A-00001", auth: "PA-1002", dx: "G4489",
					lines: []svc{{"70551", 900, "20260306"}}},
			}}),
		},
		{
			ID: "void", Title: "Void of a paid claim", Filename: "CLEARPATH_000000107.837",
			Summary: "Frequency 8 for 26064-A-00001, a paid claim.",
			Text: file("000000107", "20260316", "0625", txn{control: "0001", claims: []clm{
				{pcn: "PAT-40388", card: "ACM200101", subscriber: priya, billing: chen, freq: "8", ref: "26064-A-00001", dx: "E119",
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
