package claimsintake

import "fmt"

// WorkloadFile is one 837P file of the Journey sandbox's starting
// workload: what the clearinghouse delivered that morning.
type WorkloadFile struct {
	// Day of March 2026 the file arrives on.
	Day      int
	Filename string
	Text     string
}

// Workload is two weeks of claims for the Journey sandbox, one file per
// day that has claims. The comments give each claim's number (two-digit
// year, day of year received, E for EDI, sequence in the file) and how it
// comes out; Journey's own actions (payment runs, an adjustment, a reopen)
// and the samples refer to claims by those numbers.
//
// The claims use the situational parts of the 837P the way a mix of
// providers would: addresses throughout, and here and there a second
// diagnosis, a referral, an accident, a note, or another payer that has
// already paid.
func Workload() []WorkloadFile {
	days := []struct {
		day    int
		claims []clm
	}{
		{2, []clm{
			// 26061-E-00001 approved
			{pcn: "RIV-87811", card: "ACM100101", subscriber: jordan, billing: chen, freq: "1", dx: "J069", lines: []svc{{"99213", 180, "20260226"}},
				extras: extras{moreDx: []string{"R05"}, mrn: "MRN-10041"}},
		}},
		{3, []clm{
			// 26062-E-00001 approved
			{pcn: "OKA-10310", card: "ACM300101", subscriber: chidi, billing: chen, freq: "1", dx: "M545", lines: []svc{{"99213", 180, "20260302"}},
				extras: extras{mrn: "MRN-30012"}},
			// 26062-E-00002 denied: coverage ended 2/28
			{pcn: "MOR-20114", card: "ACM400101", subscriber: lucia, billing: chen, freq: "1", dx: "I10", lines: []svc{{"99214", 250, "20260302"}},
				extras: extras{moreDx: []string{"E785"}}},
		}},
		{4, []clm{
			// 26063-E-00001 surgery, adjusted on the 12th
			{pcn: "RIV-87990", card: "ACM100101", subscriber: jordan, billing: chen, freq: "1", dx: "M2321", lines: []svc{{"29881", 2500, "20260303"}},
				extras: extras{onset: "20260214", referring: &okonkwo, mrn: "MRN-10041", note: "ARTHROSCOPY RIGHT KNEE WITH MEDIAL MENISCECTOMY"}},
		}},
		{5, []clm{
			// 26064-E-00001 approved, paid on the 5th
			{pcn: "PAT-40388", card: "ACM200101", subscriber: priya, billing: chen, freq: "1", dx: "E119", lines: []svc{{"99214", 250, "20260304"}},
				extras: extras{moreDx: []string{"I10"}, mrn: "MRN-20007"}},
		}},
		{6, []clm{
			// 26065-E-00001: decided against the deductible the surgery
			// met, before the surgery was adjusted on the 12th
			{pcn: "RIV-88014", card: "ACM100101", subscriber: jordan, billing: chen, freq: "1", auth: "PA-1001", dx: "M2321", lines: []svc{{"70551", 900, "20260305"}},
				extras: extras{referring: &okonkwo, mrn: "MRN-10041"}},
		}},
		{9, []clm{
			// 26068-E-00001 pend: prior auth
			{pcn: "RIV-88102", card: "ACM100102", subscriber: sam, billing: chen, freq: "1", dx: "G4489", lines: []svc{{"70551", 900, "20260306"}},
				extras: extras{onset: "20260220", note: "PERSISTENT HEADACHE, 2 WEEKS"}},
		}},
		{10, []clm{
			// 26069-E-00001 pend: duplicate of 26062-E-00001
			{pcn: "OKA-10310", card: "ACM300101", subscriber: chidi, billing: chen, freq: "1", dx: "M545", lines: []svc{{"99213", 180, "20260302"}},
				extras: extras{mrn: "MRN-30012"}},
			// 26069-E-00002 emergency room after a fall, approved
			{pcn: "OKA-10477", card: "ACM300101", subscriber: chidi, patient: &ada, billing: chen, freq: "1", dx: "S0181XA", lines: []svc{{"99284", 1800, "20260308"}},
				extras: extras{pos: "23", moreDx: []string{"W06XXXA"}, accident: "20260308", emergency: true, note: "FELL FROM BED, LACERATION TO FOREHEAD"}},
		}},
		{11, []clm{
			// 26070-E-00001 pend: no benefit match
			{pcn: "PAT-40622", card: "ACM200102", subscriber: arjun, billing: chen, freq: "1", dx: "M6281", lines: []svc{{"97110", 120, "20260309"}},
				extras: extras{onset: "20260201", referring: &okonkwo, referral: "REF-1187"}},
		}},
		{12, []clm{
			// 26071-E-00001 pend: newborn not enrolled
			{pcn: "RIV-88190", card: "ACM100101", subscriber: jordan, patient: &avery, billing: chen, freq: "1", dx: "Z00110", lines: []svc{{"99213", 180, "20260310"}},
				extras: extras{note: "NEWBORN, FIRST OFFICE VISIT"}},
		}},
		{13, []clm{
			// 26072-E-00001 out-of-network
			{pcn: "OKA-10533", card: "ACM300101", subscriber: chidi, billing: ortiz, freq: "1", dx: "M2351", lines: []svc{{"29881", 2500, "20260311"}},
				extras: extras{onset: "20260222", note: "ARTHROSCOPY LEFT KNEE"}},
			// 26072-E-00002 reopened out of the payment run; the other
			// parent's plan is primary and has paid
			{pcn: "RIV-88207", card: "ACM100101", subscriber: jordan, patient: &riley, billing: chen, freq: "1", dx: "J029", lines: []svc{{"99214", 250, "20260312"}},
				extras: extras{secondary: true, other: &cob{
					payer: "BLUE RIVER HEALTH", payerID: "BRH01", subscriber: sam, memberID: "BLU5550099", relationship: "19", group: "GRP7781",
					paid: 150, remaining: 30, processed: "20260312", claimNumber: "BRH-2026-118411",
				}}},
		}},
		{16, []clm{
			// 26075-E-00001 pend: prior auth
			{pcn: "PAT-40802", card: "ACM200101", subscriber: priya, billing: chen, freq: "1", dx: "R519", lines: []svc{{"70551", 900, "20260313"}},
				extras: extras{onset: "20260301"}},
		}},
	}
	var out []WorkloadFile
	for _, d := range days {
		for i := range d.claims {
			d.claims[i].addresses = true
		}
		// The clearinghouse batches the evening before.
		icn := fmt.Sprintf("%09d", d.day)
		out = append(out, WorkloadFile{
			Day: d.day, Filename: "CLEARPATH_" + icn + ".837",
			Text: file(icn, fmt.Sprintf("202603%02d", d.day-1), "2210", txn{control: "0001", claims: d.claims}),
		})
	}
	return out
}
