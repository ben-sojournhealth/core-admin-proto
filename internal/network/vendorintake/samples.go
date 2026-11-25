package vendorintake

import (
	"fmt"
	"math/rand"
	"strings"
)

// Sample is a made-up vendor file for the demo and the tests. The layout
// is the placeholder one; the content is generated, the same every time.
type Sample struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	SourceID string `json:"sourceId"`
	Filename string `json:"filename"`
	Text     string `json:"text"`
}

// sampleRow is one row of a generated file.
type sampleRow struct {
	npi, tin, last, first, group string
	specialties                  []string
	addr, city, state, zip       string
	phone, start, term           string
	accepting, degree            string
}

var sampleHeader = "NPI,TIN,LAST_NAME,FIRST_NAME,GROUP_NAME,SPECIALTY_1,SPECIALTY_2,SPECIALTY_3,SPECIALTY_4,ADDRESS_1,CITY,STATE,ZIP,PHONE,EFF_DATE,TERM_DATE,ACCEPTING_NEW_PATIENTS,DEGREE"

// lines writes a pair's rows: one per period, and more when it has over
// four specialties.
func (r sampleRow) lines() []string {
	var out []string
	spec := r.specialties
	for first := true; first || len(spec) > 0; first = false {
		four := make([]string, 4)
		n := copy(four, spec)
		spec = spec[n:]
		out = append(out, strings.Join([]string{r.npi, r.tin, r.last, r.first, r.group, four[0], four[1], four[2], four[3],
			r.addr, r.city, r.state, r.zip, r.phone, r.start, r.term, r.accepting, r.degree}, ","))
	}
	return out
}

func render(rows []sampleRow) string {
	var b strings.Builder
	b.WriteString(sampleHeader + "\n")
	for _, r := range rows {
		for _, l := range r.lines() {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

var (
	sampleLast  = []string{"Abara", "Bennett", "Castillo", "Dang", "Eriksen", "Farouk", "Goldberg", "Haddad", "Ito", "Jensen", "Kapoor", "Lindqvist", "Morales", "Nwosu", "Okafor", "Patel", "Quinn", "Rossi", "Sandoval", "Tran", "Ueda", "Vargas", "Whitfield", "Yilmaz", "Zamora"}
	sampleFirst = []string{"Amara", "Ben", "Carmen", "Dev", "Elena", "Farid", "Grace", "Hiro", "Ines", "Jamal", "Kira", "Luis", "Maya", "Noor", "Omar", "Priya", "Rafael", "Sofia", "Tomas", "Uma"}
	sampleSpec  = []string{"Family Medicine", "Internal Medicine", "Pediatrics", "Cardiology", "Dermatology", "Orthopedic Surgery", "Obstetrics & Gynecology", "Psychiatry", "Gastroenterology", "Neurology", "Endocrinology", "Sports Medicine", "Geriatric Medicine", "Pulmonology"}
	sampleTowns = [][3]string{{"Dayton", "OH", "45402"}, {"Columbus", "OH", "43215"}, {"Cincinnati", "OH", "45202"}, {"Louisville", "KY", "40202"}, {"Lexington", "KY", "40507"}, {"Indianapolis", "IN", "46204"}, {"Fort Wayne", "IN", "46802"}, {"Pittsburgh", "PA", "15222"}, {"Detroit", "MI", "48226"}}
	sampleGroup = []string{"Family Medicine", "Medical Group", "Physicians", "Health Partners", "Clinic", "Specialists", "Associates"}
)

// sampleNetwork is the made-up network at the first file: about 60
// groups (TINs), 500 providers, 600 NPI and TIN pairs.
func sampleNetwork() []sampleRow {
	rng := rand.New(rand.NewSource(28))
	type group struct{ tin, name, addr, city, state, zip, phone string }
	var groups []group
	for i := 0; i < 60; i++ {
		t := sampleTowns[rng.Intn(len(sampleTowns))]
		groups = append(groups, group{
			tin:  fmt.Sprintf("31%07d", 1000000+i*7919),
			name: fmt.Sprintf("%s %s %s", t[0], sampleLast[rng.Intn(len(sampleLast))], sampleGroup[rng.Intn(len(sampleGroup))]),
			addr: fmt.Sprintf("%d %s St", 100+rng.Intn(900), []string{"Main", "Oak", "Market", "High", "Elm", "Walnut"}[rng.Intn(6)]),
			city: t[0], state: t[1], zip: t[2], phone: fmt.Sprintf("555-%04d", 1000+i),
		})
	}
	var rows []sampleRow
	for i := 0; i < 500; i++ {
		npi := fmt.Sprintf("1%09d", 500000000+i*104729)
		last, first := sampleLast[rng.Intn(len(sampleLast))], sampleFirst[rng.Intn(len(sampleFirst))]
		var spec []string
		// Most have one or two specialties; a few have more than a row holds.
		n := 1 + rng.Intn(2)
		if rng.Intn(25) == 0 {
			n = 5 + rng.Intn(2)
		}
		for _, j := range rng.Perm(len(sampleSpec))[:n] {
			spec = append(spec, sampleSpec[j])
		}
		// One in five bills under a second TIN.
		tins := 1
		if rng.Intn(5) == 0 {
			tins = 2
		}
		for _, g := range rng.Perm(len(groups))[:tins] {
			gr := groups[g]
			r := sampleRow{npi: npi, tin: gr.tin, last: last, first: first, group: gr.name, specialties: spec,
				addr: gr.addr, city: gr.city, state: gr.state, zip: gr.zip, phone: gr.phone,
				start: fmt.Sprintf("%d-%02d-01", 2015+rng.Intn(10), 1+rng.Intn(12)), accepting: "Y", degree: "MD"}
			// A few left and came back: the earlier period is a row of its own.
			if rng.Intn(30) == 0 {
				past := r
				past.start, past.term = "2012-03-01", "2014-06-30"
				rows = append(rows, past)
			}
			rows = append(rows, r)
		}
	}
	return rows
}

// nextMonth is the network a month on: providers joined, termed, moved,
// changed specialties; a group was renamed; some pairs are simply gone.
func nextMonth(rows []sampleRow) []sampleRow {
	var out []sampleRow
	renamed := rows[40].tin
	for i, r := range rows {
		switch {
		case i%45 == 7: // gone from the file
			continue
		case i%40 == 3 && r.term == "": // termed
			r.term = "2026-10-31"
		case i%60 == 11: // moved
			r.addr, r.phone = "2200 Commerce Pkwy", "555-7700"
		case i%70 == 5: // specialty added
			r.specialties = append(append([]string{}, r.specialties...), "Urgent Care")
		case i%90 == 13: // a column we don't map
			r.accepting = "N"
		}
		if r.tin == renamed {
			r.group += " LLC"
		}
		out = append(out, r)
	}
	for i := 0; i < 14; i++ { // joined
		src := rows[(i*23)%len(rows)]
		out = append(out, sampleRow{npi: fmt.Sprintf("1%09d", 700000000+i*7103), tin: src.tin, last: sampleLast[(i*7)%len(sampleLast)], first: sampleFirst[(i*3)%len(sampleFirst)],
			group: src.group, specialties: []string{sampleSpec[i%len(sampleSpec)]}, addr: src.addr, city: src.city, state: src.state, zip: src.zip, phone: src.phone,
			start: "2026-11-01", accepting: "Y", degree: "DO"})
	}
	return out
}

// Samples are the demo's files, meant to be sent in order.
func Samples() []Sample {
	first := sampleNetwork()
	second := nextMonth(first)
	// Two rows that can't be used.
	bad := render(second) + ",319999999,Nobody,Ann,No NPI Group,Family Medicine,,,,1 Main St,Dayton,OH,45402,555-0000,2026-11-01,,Y,MD\n" +
		"1999999999,319999999,Baddate,Bo,Bad Date Group,Family Medicine,,,,1 Main St,Dayton,OH,45402,555-0000,soon,,Y,MD\n"
	// A file cut short in transit: valid as far as it goes.
	cut := strings.SplitAfter(render(second), "\n")
	truncated := strings.Join(cut[:len(cut)*2/5], "")
	// The OAP network is narrower.
	var oap []sampleRow
	for i, r := range first {
		if i%3 != 0 {
			oap = append(oap, r)
		}
	}
	return []Sample{
		{ID: "ppo-first", Title: "PPO · first file", Summary: "The whole network, history included", SourceID: "cigna-ppo-individual", Filename: "cigna_ppo_indiv_202610.csv", Text: render(first)},
		{ID: "ppo-next", Title: "PPO · next month", Summary: "Joins, terms, moves, a renamed group, dropped rows", SourceID: "cigna-ppo-individual", Filename: "cigna_ppo_indiv_202611.csv", Text: bad},
		{ID: "ppo-truncated", Title: "PPO · cut short", Summary: "The same file, 40% of it", SourceID: "cigna-ppo-individual", Filename: "cigna_ppo_indiv_202611_partial.csv", Text: truncated},
		{ID: "oap-first", Title: "OAP · first file", Summary: "A narrower network, same providers", SourceID: "cigna-oap-individual", Filename: "cigna_oap_indiv_202610.csv", Text: render(oap)},
		{ID: "bad-layout", Title: "Wrong layout", Summary: "No TIN column", SourceID: "cigna-ppo-individual", Filename: "cigna_other.csv", Text: "NPI,NAME,EFF_DATE\n1500000000,Abara,2020-01-01\n"},
	}
}
