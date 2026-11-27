package enrollmentintake

import "fmt"

// LargeRoster is a bigger Acme population for the demos, as three more
// 834s. Two are from Acme's HR system: a change file adding about a
// hundred employees and their families (most covered from January 1, some
// hired since and a few starting April 1), and a later one ending some
// families' coverage with their employment, at the end of January or
// February. The third is from the COBRA vendor: most of those who left
// continue on COBRA from the day after, some without their dependents.
// Load them after the baselines: they are change files, so they add to
// the people open enrollment enrolled.
//
// Everyone is made up, by arithmetic on the lists below, so every load
// gives the same people. SSNs start with 9, which are never issued.
func LargeRoster() []Sample {
	var adds, terms, cobra []member
	employees := 0
	for i := 0; i < rosterEmployees; i++ {
		fam := rosterFamily(i)
		start, hired := "20260101", fmt.Sprintf("20%02d%02d15", 10+i%15, 1+i%12)
		switch {
		case i%13 == 5:
			start, hired = "20260301", "20260209"
		case i%17 == 3:
			start, hired = "20260401", "20260309"
		}
		for _, m := range fam {
			m.maint, m.reason = "021", "28"
			m.dates = [][2]string{{"356", start}}
			if m.sub {
				m.employment = "FT"
				m.dates = [][2]string{{"336", hired}, {"356", start}}
			}
			m.cov = &coverage{maint: "021", level: rosterLevel(i), dates: [][2]string{{"348", start}}}
			adds = append(adds, m)
		}
		employees++
		// Some leave at the end of January or February, with their
		// families.
		if i%7 != 4 || start != "20260101" {
			continue
		}
		end, next := "20260228", "20260301"
		if i%2 == 0 {
			end, next = "20260131", "20260201"
		}
		for _, m := range fam {
			m.maint, m.reason = "024", "08"
			m.dates = [][2]string{{"357", end}}
			if m.sub {
				m.employment = "TE"
				m.dates = [][2]string{{"337", end}, {"357", end}}
			}
			m.cov = &coverage{maint: "024", level: rosterLevel(i), dates: [][2]string{{"349", end}}}
			terms = append(terms, m)
		}
		// Most of them elect COBRA, from the day after; every third
		// family doesn't, and some continue the employee alone.
		if i%3 == 0 {
			continue
		}
		level, continuing := rosterLevel(i), fam
		if i%4 == 3 {
			level, continuing = "EMP", fam[:1]
		}
		for _, m := range continuing {
			m.maint, m.status, m.cobraEvent = "021", "C", "1"
			m.dates = [][2]string{{"340", next}, {"356", next}}
			if m.sub {
				m.employment = "TE"
			}
			m.cov = &coverage{maint: "021", level: level, dates: [][2]string{{"348", next}}}
			cobra = append(cobra, m)
		}
	}
	return []Sample{
		{ID: "roster-adds", Title: "Acme: the rest of the workforce",
			Summary:  fmt.Sprintf("A change file adding %d employees and their families.", employees),
			Filename: "ACMEHR_000000190.834",
			Text:     file("000000190", "20251219", "0600", txn{control: "0001", action: "2", members: adds})},
		{ID: "roster-terms", Title: "Acme: terminations",
			Summary:  "A change file ending some families' coverage with their employment, at the end of January or February.",
			Filename: "ACMEHR_000000191.834",
			Text:     file("000000191", "20260302", "0600", txn{control: "0001", action: "2", members: terms})},
		{ID: "roster-cobra", Title: "Acme: COBRA elections",
			Summary:  "The COBRA vendor's change file: most of those who left continue on COBRA.",
			Filename: "COBRAVND_000000310.834",
			Text:     fileFrom(COBRAVendor, "000000310", "20260310", "0600", txn{control: "0001", sponsor: acme, action: "2", members: cobra})},
	}
}

const rosterEmployees = 104

var (
	rosterLasts = []string{"ABARA", "BAKER", "CASTILLO", "DELGADO", "EDWARDS", "FISCHER", "GUPTA", "HARRIS", "IBARRA", "JENSEN",
		"KOWALSKI", "LARSEN", "MARTIN", "NAKAMURA", "OYELARAN", "PETROV", "QUINN", "REYES", "SANTOS", "TANAKA",
		"UDOH", "VARGAS", "WALKER", "XIONG", "YOUNG", "ZIELINSKI", "ANDERSEN", "BRENNAN", "COHEN", "DIAZ",
		"ELLIS", "FOSTER", "GREENE", "HOFFMAN", "IVANOV", "JOSHI", "KELLER"}
	rosterFirsts = []string{"AALIYAH", "BEN", "CARMEN", "DARIUS", "ELENA", "FRANK", "GRACE", "HASSAN", "INGRID", "JAMAL",
		"KAREN", "LUIS", "MAYA", "NIKHIL", "OLIVIA", "PAUL", "ROSA", "STEFAN", "TANYA", "VICTOR", "WENDY", "YUSUF", "ZOE"}
	rosterSpouses = []string{"ADRIAN", "BIANCA", "CALEB", "DANA", "EMIL", "FATIMA", "GAVIN", "HELEN", "ISAAC", "JULIA",
		"KOFI", "LENA", "MARCO", "NADIA", "OMAR", "PAIGE", "RAFAEL", "SONIA", "TOMAS"}
	rosterKids = []string{"AVA", "BODHI", "CLARA", "DEV", "EZRA", "FLORA", "GUS", "HANA", "IVY", "JONAH", "KAI", "LILA", "MILO", "NORA"}
)

// rosterLevel is the tier employee i elects: about four in ten employee
// only, the rest split between spouse, children and family.
func rosterLevel(i int) string {
	return []string{"EMP", "FAM", "EMP", "ESP", "FAM", "EMP", "ECH", "EMP", "ESP", "FAM"}[i%10]
}

// rosterFamily is employee i and the dependents their tier covers, with
// no maintenance set.
func rosterFamily(i int) []member {
	sub := fmt.Sprintf("E2%04d", i+1)
	last := rosterLasts[(i*7+3)%len(rosterLasts)]
	ssn := func(n int) string { return fmt.Sprintf("9992%03d%02d", i+1, n) }
	sex := func(k int) string { return []string{"F", "M"}[k%2] }
	fam := []member{{sub: true, rel: "18", subNumber: sub, last: last, first: rosterFirsts[(i*5+1)%len(rosterFirsts)], ssn: ssn(1),
		dob: fmt.Sprintf("19%02d%02d%02d", 62+(i*7)%36, 1+(i*5)%12, 1+(i*11)%28), sex: sex(i),
		street: fmt.Sprintf("%d %s", 100+(i*37)%900, []string{"OAK ST", "MAPLE AVE", "WAYNE AVE", "BROWN ST", "FIFTH ST", "LINDEN AVE"}[i%6]),
		city:   "DAYTON"}}
	level := rosterLevel(i)
	if level == "ESP" || level == "FAM" {
		fam = append(fam, member{rel: "01", subNumber: sub, last: last, first: rosterSpouses[(i*3+2)%len(rosterSpouses)], ssn: ssn(2),
			dob: fmt.Sprintf("19%02d%02d%02d", 63+(i*5)%35, 1+(i*7)%12, 1+(i*13)%28), sex: sex(i + 1)})
	}
	if level == "ECH" || level == "FAM" {
		for k := 0; k < 1+i%3; k++ {
			fam = append(fam, member{rel: "19", subNumber: sub, last: last, first: rosterKids[(i*3+k*5)%len(rosterKids)], ssn: ssn(3 + k),
				dob: fmt.Sprintf("20%02d%02d%02d", 6+(i+k*4)%18, 1+(i+k*5)%12, 1+(i*3+k*9)%28), sex: sex(i + k)})
		}
	}
	return fam
}
