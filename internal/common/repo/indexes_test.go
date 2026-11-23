package repo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/repo/repotest"
	"coreadmin/internal/common/store"
)

type explaining[T store.Resource] interface {
	Explain(ctx context.Context, opts store.ListOptions[T]) (string, error)
}

func explainer[T store.Resource](s store.Store[T]) func(c store.Cond) (string, error) {
	return func(c store.Cond) (string, error) {
		return s.(explaining[T]).Explain(context.Background(), store.ListOptions[T]{Where: []store.Cond{c}})
	}
}

// Every index in repo.Tables serves the access pattern it was made for:
// Postgres's plan for that lookup uses it.
func TestIndexesServeTheirAccessPatterns(t *testing.T) {
	repos := repotest.New(t)
	// By domain and table.
	explain := map[string]func(c store.Cond) (string, error){
		"claims.claims":                    explainer(repos.Claims),
		"employer.members":                 explainer(repos.Members),
		"employer.groups":                  explainer(repos.Groups),
		"network.providers":                explainer(repos.Providers),
		"claims.ingestion_jobs":            explainer(repos.ClaimIngestionJobs),
		"employer.ingestion_jobs":          explainer(repos.EnrollmentIngestionJobs),
		"employer.enrollment_transactions": explainer(repos.EnrollmentTransactions),
		"employer.member_versions":         explainer(repos.MemberVersions),
		"claims.remittances":               explainer(repos.Remittances),
	}
	gin := map[string]string{
		"remittances_doc": `{"claims":[{"claimId":"x7"}]}`,
	}
	// Plans depend on the data: give every indexed table 5,000 current
	// rows whose indexed fields take 50 values, and statistics on them.
	db := repos.DB.(*store.PGDB)
	ctx := context.Background()
	for _, tb := range repo.Tables {
		if len(tb.Indexes) == 0 {
			continue
		}
		doc := map[string]any{}
		for _, ix := range tb.Indexes {
			if ix.GIN {
				continue
			}
			m := doc
			for _, k := range ix.Path[:len(ix.Path)-1] {
				if m[k] == nil {
					m[k] = map[string]any{}
				}
				m = m[k].(map[string]any)
			}
			m[ix.Path[len(ix.Path)-1]] = "v@"
		}
		// Claim ids are unique, so a lookup by one matches a row or two.
		doc["claims"] = []any{map[string]any{"claimId": "x#"}}
		tmpl, _ := json.Marshal(doc)
		q := fmt.Sprintf(`INSERT INTO %s (id, revision, tx_from, recorded_by, data)
			SELECT 'r' || g, 1, now(), 'test', replace(replace($1, '@', (g %% 50)::text), '#', g::text)::jsonb FROM generate_series(1, 5000) g`, db.Qualified(tb.Schema, tb.Name))
		if _, err := db.Pool.Exec(ctx, q, string(tmpl)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(ctx, "ANALYZE "+db.Qualified(tb.Schema, tb.Name)); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, tb := range repo.Tables {
		for _, ix := range tb.Indexes {
			n++
			ex := explain[tb.Schema+"."+tb.Name]
			if ex == nil {
				t.Errorf("%s: no explainer for table %s.%s", ix.Name, tb.Schema, tb.Name)
				continue
			}
			conds := []store.Cond{store.Eq("v7", ix.Path...)}
			if ix.GIN {
				conds = []store.Cond{store.Contains(gin[ix.Name])}
			} else if ix.Name == "claims_by_number" {
				conds = append(conds, store.Prefix("v1", ix.Path...))
			}
			for _, c := range conds {
				plan, err := ex(c)
				if err != nil {
					t.Fatalf("%s: %v", ix.Name, err)
				}
				if !strings.Contains(plan, ix.Name) {
					t.Errorf("%s (%s): the plan doesn't use it:\n%s", ix.Name, ix.Why, plan)
				}
			}
		}
	}
	if n < 10 {
		t.Errorf("only %d indexes", n)
	}
}
