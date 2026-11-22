package repo_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	"coreadmin/internal/repo"
	"coreadmin/internal/repo/repotest"
	"coreadmin/internal/store"
)

// Every table is in its domain's schema, and nothing else is in the
// sandbox's schemas.
func TestTablesLiveInTheirDomainSchemas(t *testing.T) {
	repos := repotest.New(t)
	db := repos.DB.(*store.PGDB)
	want := []string{"claims.claim_number_blocks"}
	for _, tb := range repo.Tables {
		want = append(want, tb.Schema+"."+tb.Name)
	}
	rows, err := db.Pool.Query(context.Background(),
		`SELECT table_schema, table_name FROM information_schema.tables WHERE table_schema LIKE $1`, db.Prefix+`\_%`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var schema, table string
		if err := rows.Scan(&schema, &table); err != nil {
			t.Fatal(err)
		}
		got = append(got, strings.TrimPrefix(schema, db.Prefix+"_")+"."+table)
	}
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("tables:\n got %v\nwant %v", got, want)
	}
}

// 837 and 834 files are recorded by their own intakes, in their own
// domains: neither sees the other's.
func TestIngestionJobsAreSplitByDomain(t *testing.T) {
	repos := repotest.New(t)
	ctx := context.Background()
	if _, err := repos.ClaimIngestionJobs.Create(ctx, &ingestionv1.IngestionJob{Id: "job_1", OriginalFilename: "a.837"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.EnrollmentIngestionJobs.Create(ctx, &ingestionv1.IngestionJob{Id: "job_1", OriginalFilename: "a.834"}); err != nil {
		t.Fatalf("same id in the other domain: %v", err)
	}
	claims, _, _ := repos.ClaimIngestionJobs.List(ctx, store.ListOptions[*ingestionv1.IngestionJob]{})
	members, _, _ := repos.EnrollmentIngestionJobs.List(ctx, store.ListOptions[*ingestionv1.IngestionJob]{})
	if len(claims) != 1 || claims[0].GetOriginalFilename() != "a.837" || len(members) != 1 || members[0].GetOriginalFilename() != "a.834" {
		t.Errorf("claims jobs %v, enrollment jobs %v", claims, members)
	}
}
