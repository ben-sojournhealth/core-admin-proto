package repo_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"coreadmin/internal/repo"
)

// Which domain each Repos store belongs to (its table's schema).
var storeDomain = map[string]string{
	"Customers": repo.Employer, "Groups": repo.Employer, "Plans": repo.Employer, "Members": repo.Employer,
	"EnrollmentIngestionJobs": repo.Employer, "EnrollmentTransactions": repo.Employer,
	"EligibilityInputs": repo.Employer, "MemberVersions": repo.Employer,

	"Claims": repo.Claims, "FamilyAccumulators": repo.Claims, "FeeSchedules": repo.Claims,
	"ClaimIngestionJobs": repo.Claims, "Remittances": repo.Claims, "ClaimNumbers": repo.Claims,

	"Providers": repo.Network, "Networks": repo.Network, "Contracts": repo.Network,

	"StopLossPolicies": repo.Finance, "SpecificExposures": repo.Finance,
	"AggregateExposures": repo.Finance, "StopLossFilings": repo.Finance,
}

// Each domain's packages. Everything else (demos, the simulator, seed
// data, Journey) is a client of every domain and isn't checked.
var domainPackages = map[string][]string{
	repo.Employer: {"employer", "eligibility", "enrollmentintake"},
	repo.Claims:   {"claims", "adjudication", "claimsintake", "remittance"},
	repo.Network:  {"network"},
	repo.Finance:  {"stoploss"},
}

var storeUse = regexp.MustCompile(`Repos\.([A-Z][A-Za-z]*)\b`)

// A domain's code uses only its own domain's stores; it asks other
// domains through their APIs (employer.API, network.API).
func TestDomainsUseOnlyTheirOwnStores(t *testing.T) {
	for domain, pkgs := range domainPackages {
		for _, pkg := range pkgs {
			files, _ := filepath.Glob(filepath.Join("..", pkg, "*.go"))
			for _, f := range files {
				if strings.HasSuffix(f, "_test.go") {
					continue
				}
				src, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				for i, line := range strings.Split(string(src), "\n") {
					for _, m := range storeUse.FindAllStringSubmatch(line, -1) {
						owner, ok := storeDomain[m[1]]
						if ok && owner != domain {
							t.Errorf("%s:%d: %s code uses %s's store %s", f, i+1, domain, owner, m[1])
						}
					}
				}
			}
		}
	}
}

// Every store in Repos is assigned a domain above.
func TestEveryStoreHasADomain(t *testing.T) {
	src, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	field := regexp.MustCompile(`(?m)^\t([A-Z][A-Za-z]*)\s+(store\.Store|ClaimNumbers)`)
	for _, m := range field.FindAllStringSubmatch(string(src), -1) {
		if _, ok := storeDomain[m[1]]; !ok {
			t.Errorf("Repos.%s has no domain in storeDomain", m[1])
		}
	}
}
