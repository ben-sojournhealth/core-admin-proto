package demos_test

import (
	"strings"
	"testing"

	"coreadmin/internal/claims/adjudication"
	"coreadmin/internal/common/protodoc"
	"coreadmin/internal/demos"
)

// Every call on the adjudication map is a real method of a real service.
func TestAdjudicationStepCallsMatchTheProtos(t *testing.T) {
	for _, s := range adjudication.Steps {
		for _, c := range s.Calls {
			if _, err := protodoc.Interface(c.Service, c.Methods); err != nil {
				t.Errorf("%s: %v", s.ID, err)
			}
		}
	}
}

// Every line's service and methods exist in the protos, and every method
// of the domain services is on some line of the map.
func TestInterfacesMatchTheProtos(t *testing.T) {
	onALine := map[string]bool{}
	for _, in := range demos.Interfaces {
		c, err := in.Contract()
		if err != nil {
			t.Errorf("%s → %s: %v", in.From, in.To, err)
			continue
		}
		for _, m := range in.Methods() {
			onALine[in.Service+"/"+m] = true
		}
		for _, m := range c.Methods {
			if !onALine[in.Service+"/"+m.Name] && m.Used {
				t.Errorf("%s: %s used but not listed", in.Service, m.Name)
			}
		}
	}
	for _, svc := range []string{"coreadmin.employer.v1.EmployerService", "coreadmin.network.v1.NetworkService"} {
		in := demos.Interface{Service: svc}
		c, err := in.Contract()
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range c.Methods {
			if !onALine[svc+"/"+m.Name] {
				t.Errorf("%s/%s isn't on any line of the map", svc, m.Name)
			}
		}
	}
}

// The members line shows the coverage call and everything it carries:
// the member, coverage spans and dates, with their comments, by package.
func TestContractRendersWhatCrossesTheLine(t *testing.T) {
	in, ok := demos.InterfaceOn(demos.Eligibility, demos.Adjudication)
	if !ok {
		t.Fatal("no interface on eligibility → adjudication")
	}
	c, err := in.Contract()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Source, "rpc GetMedicalCoverage(GetMedicalCoverageRequest) returns (GetMedicalCoverageResponse);") {
		t.Errorf("service source:\n%s", c.Source)
	}
	var pkgs []string
	all := ""
	for _, p := range c.Packages {
		pkgs = append(pkgs, p.Name)
		for _, ty := range p.Types {
			all += ty.Source
		}
	}
	if len(pkgs) == 0 || pkgs[0] != "coreadmin.employer.v1" {
		t.Errorf("packages: %v", pkgs)
	}
	for _, want := range []string{
		"message GetMedicalCoverageResponse {",
		"  eligibility.v1.CoverageSpan span = 2;",
		"message CoverageSpan {",
		"// The date asked about, e.g. a claim's date of service.",
		"enum CoverageStatus {",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(all, "message GetPlanVersionRequest") {
		t.Error("types of methods not used on this line are shown")
	}
}
