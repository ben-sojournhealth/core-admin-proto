package repo_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"coreadmin/internal/common/repo"
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

// Each domain's code is everything under internal/<domain>/, except its
// demos (packages named *demo): like Journey, the simulator and seed data,
// demos set up every domain and aren't checked.
var storeUse = regexp.MustCompile(`Repos\.([A-Z][A-Za-z]*)\b`)

// A domain's code uses only its own domain's stores; it asks other
// domains through their APIs (employer.API, network.API).
func TestDomainsUseOnlyTheirOwnStores(t *testing.T) {
	for _, domain := range repo.Domains {
		root := filepath.Join("..", "..", domain)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if strings.HasSuffix(d.Name(), "demo") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(src), "\n") {
				for _, m := range storeUse.FindAllStringSubmatch(line, -1) {
					if owner, ok := storeDomain[m[1]]; ok && owner != domain {
						t.Errorf("%s:%d: %s code uses %s's store %s", path, i+1, domain, owner, m[1])
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", domain, err)
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
