package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	customerv1 "coreadmin/gen/coreadmin/customer/v1"
	"coreadmin/internal/store"
)

func TestMemoryCRUD(t *testing.T) {
	ctx := store.WithActor(context.Background(), "alice")
	s := store.NewMemory[*customerv1.Customer]()

	created, err := s.Create(ctx, &customerv1.Customer{Id: "c1", Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if got := created.GetAudit(); got.GetRevision() != 1 || got.GetCreatedBy() != "alice" {
		t.Fatalf("audit = %v", got)
	}
	if _, err := s.Create(ctx, &customerv1.Customer{Id: "c1"}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate create: %v", err)
	}

	// Returned values are copies.
	created.Name = "mutated"
	got, err := s.Get(ctx, "c1")
	if err != nil || got.GetName() != "Acme" {
		t.Fatalf("Get = %v, %v", got, err)
	}

	got.Name = "Acme Corp"
	updated, err := s.Update(store.WithActor(ctx, "bob"), got)
	if err != nil {
		t.Fatal(err)
	}
	if a := updated.GetAudit(); a.GetRevision() != 2 || a.GetUpdatedBy() != "bob" || a.GetCreatedBy() != "alice" {
		t.Fatalf("audit after update = %v", a)
	}

	// Stale write loses.
	if _, err := s.Update(ctx, got); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}

	if err := s.Delete(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after delete: %v", err)
	}
}

func TestMemoryListPagination(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory[*customerv1.Customer]()
	for i := range 5 {
		ft := customerv1.FundingType_FUNDING_TYPE_SELF_FUNDED
		if i%2 == 1 {
			ft = customerv1.FundingType_FUNDING_TYPE_FULLY_INSURED
		}
		if _, err := s.Create(ctx, &customerv1.Customer{Id: fmt.Sprintf("c%d", i), FundingType: ft}); err != nil {
			t.Fatal(err)
		}
	}

	selfFunded := func(c *customerv1.Customer) bool {
		return c.GetFundingType() == customerv1.FundingType_FUNDING_TYPE_SELF_FUNDED
	}
	var ids []string
	token := ""
	for {
		page, next, err := s.List(ctx, store.ListOptions[*customerv1.Customer]{Filter: selfFunded, PageSize: 2, PageToken: token})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page {
			ids = append(ids, c.GetId())
		}
		if next == "" {
			break
		}
		token = next
	}
	if fmt.Sprint(ids) != "[c0 c2 c4]" {
		t.Fatalf("ids = %v", ids)
	}
}
