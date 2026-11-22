package demos

import "coreadmin/internal/common/protodoc"

// Interface is a protobuf service on a line of the domain map: what the
// components at one end ask the domain at the other. Lines with no proto
// service (X12 files, interfaces not defined yet) have none.
type Interface struct {
	// The line's ends, as drawn: data flows From → To.
	From    string `json:"from"`
	To      string `json:"to"`
	Service string `json:"service"`
	Calls   []Call `json:"calls"`
}

// Call is the methods one component calls.
type Call struct {
	By      string   `json:"by"`
	Methods []string `json:"methods"`
}

const (
	employerService = "coreadmin.employer.v1.EmployerService"
	networkService  = "coreadmin.network.v1.NetworkService"
)

// Interfaces lists every line of the map that has a proto service.
var Interfaces = []Interface{
	{From: Eligibility, To: Adjudication, Service: employerService, Calls: []Call{
		{By: "Claim Adjudication", Methods: []string{"MatchClaimMember", "GetMedicalCoverage"}},
		{By: "Remittance & EOBs", Methods: []string{"GetMember", "GetCustomer"}},
	}},
	{From: PlanDesign, To: Adjudication, Service: employerService, Calls: []Call{
		{By: "Claim Adjudication", Methods: []string{"GetPlanVersion"}},
	}},
	{From: ProviderData, To: Adjudication, Service: networkService, Calls: []Call{
		{By: "Claim Adjudication", Methods: []string{"GetProvider"}},
	}},
	{From: Contracting, To: Adjudication, Service: networkService, Calls: []Call{
		{By: "Claim Adjudication", Methods: []string{"GetContract"}},
	}},
}

// InterfaceOn is the interface on the line from → to, if it has one.
func InterfaceOn(from, to string) (*Interface, bool) {
	for i := range Interfaces {
		if Interfaces[i].From == from && Interfaces[i].To == to {
			return &Interfaces[i], true
		}
	}
	return nil, false
}

// Methods is every method called over the line.
func (in *Interface) Methods() []string {
	var all []string
	for _, c := range in.Calls {
		all = append(all, c.Methods...)
	}
	return all
}

// Contract is the interface's service as rendered proto source.
func (in *Interface) Contract() (*protodoc.Contract, error) {
	return protodoc.Interface(in.Service, in.Methods())
}
