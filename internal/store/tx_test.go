package store_test

import (
	"context"
	"errors"
	"testing"

	customerv1 "coreadmin/gen/coreadmin/customer/v1"
	"coreadmin/internal/store"
)

type cust = *customerv1.Customer

// twoStores returns two stores in one database, so a transaction spans
// both.
func twoStores() (*store.MemoryDB, *store.Memory[cust], *store.Memory[*customerv1.Group]) {
	db := store.NewMemoryDB()
	return db, store.NewMemoryIn[cust](db), store.NewMemoryIn[*customerv1.Group](db)
}

func TestTxCommitsAcrossStores(t *testing.T) {
	ctx := context.Background()
	db, cs, gs := twoStores()
	err := db.InTx(ctx, func(ctx context.Context) error {
		if _, err := cs.Create(ctx, &customerv1.Customer{Id: "c1", Name: "Acme"}); err != nil {
			return err
		}
		if _, err := gs.Create(ctx, &customerv1.Group{Id: "g1", CustomerId: "c1"}); err != nil {
			return err
		}
		// Own writes are visible inside the transaction...
		if c, err := cs.Get(ctx, "c1"); err != nil || c.GetName() != "Acme" {
			t.Errorf("read own write: %v %v", c, err)
		}
		if l, _, _ := gs.List(ctx, store.ListOptions[*customerv1.Group]{}); len(l) != 1 {
			t.Errorf("list own writes: %d", len(l))
		}
		// ...and to no one else until commit.
		if _, err := cs.Get(context.Background(), "c1"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("uncommitted write leaked: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Get(ctx, "c1"); err != nil {
		t.Error(err)
	}
	if _, err := gs.Get(ctx, "g1"); err != nil {
		t.Error(err)
	}
}

func TestTxErrorKeepsNothing(t *testing.T) {
	ctx := context.Background()
	db, cs, gs := twoStores()
	seed, _ := cs.Create(ctx, &customerv1.Customer{Id: "c1", Name: "Acme"})
	boom := errors.New("boom")
	err := db.InTx(ctx, func(ctx context.Context) error {
		seed.Name = "Changed"
		if _, err := cs.Update(ctx, seed); err != nil {
			return err
		}
		if err := cs.Delete(ctx, "c1"); err != nil {
			return err
		}
		if _, err := gs.Create(ctx, &customerv1.Group{Id: "g1"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if c, err := cs.Get(ctx, "c1"); err != nil || c.GetName() != "Acme" || c.GetAudit().GetRevision() != 1 {
		t.Errorf("customer after rollback: %v %v", c, err)
	}
	if _, err := gs.Get(ctx, "g1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("group after rollback: %v", err)
	}
}

// A record changed by someone else after this transaction read it fails
// the commit, and none of the transaction's other writes land.
func TestTxConflictAtCommit(t *testing.T) {
	ctx := context.Background()
	db, cs, gs := twoStores()
	cs.Create(ctx, &customerv1.Customer{Id: "c1", Name: "Acme"})
	err := db.InTx(ctx, func(tctx context.Context) error {
		c, _ := cs.Get(tctx, "c1")
		c.Name = "Mine"
		if _, err := cs.Update(tctx, c); err != nil {
			return err
		}
		gs.Create(tctx, &customerv1.Group{Id: "g1"})
		// Another writer commits first.
		other, _ := cs.Get(ctx, "c1")
		other.Name = "Theirs"
		if _, err := cs.Update(ctx, other); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want conflict", err)
	}
	if c, _ := cs.Get(ctx, "c1"); c.GetName() != "Theirs" {
		t.Errorf("name = %s", c.GetName())
	}
	if _, err := gs.Get(ctx, "g1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("group from the failed transaction landed: %v", err)
	}
}

func TestTxCreateRace(t *testing.T) {
	ctx := context.Background()
	db, cs, _ := twoStores()
	err := db.InTx(ctx, func(tctx context.Context) error {
		cs.Create(tctx, &customerv1.Customer{Id: "c1", Name: "Mine"})
		_, err := cs.Create(ctx, &customerv1.Customer{Id: "c1", Name: "Theirs"})
		return err
	})
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("err = %v", err)
	}
}

func TestTxNestedJoins(t *testing.T) {
	ctx := context.Background()
	db, cs, _ := twoStores()
	boom := errors.New("boom")
	err := db.InTx(ctx, func(ctx context.Context) error {
		if err := db.InTx(ctx, func(ctx context.Context) error {
			_, err := cs.Create(ctx, &customerv1.Customer{Id: "c1"})
			return err
		}); err != nil {
			return err
		}
		// The inner call did not commit on its own.
		if _, err := cs.Get(context.Background(), "c1"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("inner transaction committed early: %v", err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := cs.Get(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("c1 survived the outer rollback: %v", err)
	}
}

func TestTxContextIsDeadAfterwards(t *testing.T) {
	ctx := context.Background()
	db, cs, _ := twoStores()
	var leaked context.Context
	db.InTx(ctx, func(ctx context.Context) error { leaked = ctx; return nil })
	if _, err := cs.Create(leaked, &customerv1.Customer{Id: "c1"}); !errors.Is(err, store.ErrTxDone) {
		t.Errorf("write on a finished transaction: %v", err)
	}
}

// Updating the same record twice in a transaction commits once, at the
// final revision, checked against the revision first read.
func TestTxRepeatedUpdates(t *testing.T) {
	ctx := context.Background()
	db, cs, _ := twoStores()
	cs.Create(ctx, &customerv1.Customer{Id: "c1", Name: "A"})
	err := db.InTx(ctx, func(ctx context.Context) error {
		for _, n := range []string{"B", "C"} {
			c, _ := cs.Get(ctx, "c1")
			c.Name = n
			if _, err := cs.Update(ctx, c); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := cs.Get(ctx, "c1"); c.GetName() != "C" || c.GetAudit().GetRevision() != 3 {
		t.Errorf("got %s rev %d", c.GetName(), c.GetAudit().GetRevision())
	}
}

// Two transactions that lock the same record can't both commit, even
// though neither changes it: the second must start over.
func TestLockSerializesDecisions(t *testing.T) {
	ctx := context.Background()
	db, cs, gs := twoStores()
	cs.Create(ctx, &customerv1.Customer{Id: "c1", Name: "Acme"})
	// Each transaction adds a group only if the customer has none yet:
	// the check is a read, so only the lock makes it safe.
	addFirstGroup := func(ctx context.Context, id string) error {
		if err := cs.Lock(ctx, "c1"); err != nil {
			return err
		}
		existing, _, _ := gs.List(ctx, store.ListOptions[*customerv1.Group]{})
		if len(existing) > 0 {
			return nil
		}
		_, err := gs.Create(ctx, &customerv1.Group{Id: id, CustomerId: "c1"})
		return err
	}
	err := db.InTx(ctx, func(tctx context.Context) error {
		if err := addFirstGroup(tctx, "g1"); err != nil {
			return err
		}
		// Another transaction makes the same decision and commits first.
		return db.InTx(context.Background(), func(ctx context.Context) error { return addFirstGroup(ctx, "g2") })
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want conflict", err)
	}
	// Retried, the loser sees the winner's group and adds nothing.
	if err := db.InTx(ctx, func(ctx context.Context) error { return addFirstGroup(ctx, "g1") }); err != nil {
		t.Fatal(err)
	}
	if l, _, _ := gs.List(ctx, store.ListOptions[*customerv1.Group]{}); len(l) != 1 || l[0].GetId() != "g2" {
		t.Errorf("groups = %v", l)
	}
	// A lock changes nothing about the record.
	if c, _ := cs.Get(ctx, "c1"); c.GetName() != "Acme" || c.GetAudit().GetRevision() != 1 {
		t.Errorf("customer after locks: %v", c)
	}
}

func TestLockThenUpdate(t *testing.T) {
	ctx := context.Background()
	db, cs, _ := twoStores()
	cs.Create(ctx, &customerv1.Customer{Id: "c1", Name: "A"})
	err := db.InTx(ctx, func(ctx context.Context) error {
		if err := cs.Lock(ctx, "c1"); err != nil {
			return err
		}
		c, _ := cs.Get(ctx, "c1")
		c.Name = "B"
		_, err := cs.Update(ctx, c)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := cs.Get(ctx, "c1"); c.GetName() != "B" || c.GetAudit().GetRevision() != 2 {
		t.Errorf("got %v", c)
	}
	if err := cs.Lock(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("lock of a missing record: %v", err)
	}
}
