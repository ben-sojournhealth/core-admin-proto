package repo_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"coreadmin/internal/claims/claimsdb"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/employer/employerdb"
	"coreadmin/internal/finance/financedb"
	"coreadmin/internal/network/networkdb"
)

// Which domain each Repos store belongs to: the domain whose <domain>db
// package declares it.
var storeDomain = func() map[string]string {
	owners := map[reflect.Type]string{
		reflect.TypeOf(employerdb.EmployerStores{}): employerdb.Schema,
		reflect.TypeOf(claimsdb.ClaimsStores{}):     claimsdb.Schema,
		reflect.TypeOf(networkdb.NetworkStores{}):   networkdb.Schema,
		reflect.TypeOf(financedb.FinanceStores{}):   financedb.Schema,
	}
	m := map[string]string{}
	for _, f := range reflect.VisibleFields(reflect.TypeOf(repo.Repos{})) {
		if len(f.Index) == 2 {
			parent := reflect.TypeOf(repo.Repos{}).FieldByIndex(f.Index[:1]).Type
			if d, ok := owners[parent]; ok {
				m[f.Name] = d
			}
		}
	}
	return m
}()

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

// Every domain's stores are in Repos, and every embedded group of stores
// belongs to a domain.
func TestEveryStoreHasADomain(t *testing.T) {
	for _, f := range reflect.VisibleFields(reflect.TypeOf(repo.Repos{})) {
		if f.Anonymous {
			continue
		}
		if len(f.Index) == 1 && f.Name != "DB" {
			t.Errorf("Repos.%s isn't declared by a domain's db package", f.Name)
		}
		if len(f.Index) == 2 && storeDomain[f.Name] == "" {
			t.Errorf("Repos.%s has no domain", f.Name)
		}
	}
	if len(storeDomain) < 15 {
		t.Errorf("only %d stores found: %v", len(storeDomain), storeDomain)
	}
}
