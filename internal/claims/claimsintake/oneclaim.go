package claimsintake

import "time"

// OneClaim is a one-line professional claim to write as an 837P file of
// its own: how the simulator submits a claim, so that every claim in a
// sandbox arrives the way a clearinghouse would deliver it.
type OneClaim struct {
	// Interchange control number (nine digits), unique per file.
	ICN string
	// When the clearinghouse created the file.
	Sent time.Time
	// Patient control number.
	PCN string

	// The subscriber as the provider identifies them.
	MemberNumber, Last, First string
	Born                      string // YYYYMMDD
	Sex                       string // F, M or U

	// The billing provider, which is also sent as the rendering provider.
	// Org is set for an organization, ProviderLast and ProviderFirst for a
	// person.
	Org, ProviderLast, ProviderFirst string
	NPI, TIN, Taxonomy               string

	// 1 original, 7 replacement, 8 void; Quoted is the claim number a
	// replacement or void names (REF*F8).
	Frequency, Quoted string
	Authorization     string
	Diagnosis         string // ICD-10-CM, no decimal point

	Procedure      string
	PlaceOfService string
	ServiceDate    string // YYYYMMDD
	ChargeCents    int64
}

// Filename is the file's name, from its control number.
func (o OneClaim) Filename() string { return "SIM_" + o.ICN + ".837" }

// Text is the file.
func (o OneClaim) Text() string {
	return file(o.ICN, o.Sent.Format("20060102"), o.Sent.Format("1504"), txn{control: "0001", claims: []clm{{
		pcn: o.PCN, card: o.MemberNumber,
		subscriber: person{last: o.Last, first: o.First, dob: o.Born, sex: o.Sex},
		billing:    prov{last: o.ProviderLast, first: o.ProviderFirst, org: o.Org, npi: o.NPI, tin: o.TIN, taxonomy: o.Taxonomy},
		freq:       o.Frequency, ref: o.Quoted, auth: o.Authorization, dx: o.Diagnosis,
		lines:  []svc{{proc: o.Procedure, dos: o.ServiceDate}},
		extras: extras{pos: o.PlaceOfService, cents: o.ChargeCents, renderOrg: true},
	}}})
}
