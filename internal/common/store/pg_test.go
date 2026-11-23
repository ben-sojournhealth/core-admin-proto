package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	customerv1 "coreadmin/gen/coreadmin/customer/v1"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/repo/repotest"
	"coreadmin/internal/common/store"
)

func TestCRUD(t *testing.T) {
	repos := repotest.New(t)
	ctx := store.WithActor(context.Background(), "alice")
	cs := repos.Customers
	c, err := cs.Create(ctx, &customerv1.Customer{Id: "c1", Name: "Acme"})
	if err != nil || c.GetAudit().GetRevision() != 1 || c.GetAudit().GetCreatedBy() != "alice" {
		t.Fatalf("create: %v %v", c, err)
	}
	if _, err := cs.Create(ctx, &customerv1.Customer{Id: "c1"}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Errorf("duplicate: %v", err)
	}
	c.Name = "Acme Corp"
	u, err := cs.Update(store.WithActor(ctx, "bob"), c)
	if err != nil || u.GetAudit().GetRevision() != 2 || u.GetAudit().GetCreatedBy() != "alice" || u.GetAudit().GetUpdatedBy() != "bob" {
		t.Fatalf("update: %v %v", u, err)
	}
	if _, err := cs.Update(ctx, c); !errors.Is(err, store.ErrConflict) {
		t.Errorf("stale update: %v", err)
	}
	if got, _ := cs.Get(ctx, "c1"); got.GetName() != "Acme Corp" {
		t.Errorf("get: %v", got)
	}
	if err := cs.Delete(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Get(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

// Nothing is overwritten: every version stays, one valid at a time, and
// a deleted record's history continues if it's created again.
func TestHistory(t *testing.T) {
	repos := repotest.New(t)
	ctx := context.Background()
	cs := repos.Customers
	c, _ := cs.Create(store.WithActor(ctx, "alice"), &customerv1.Customer{Id: "c1", Name: "A"})
	for _, n := range []string{"B", "C"} {
		c.Name = n
		var err error
		if c, err = cs.Update(store.WithActor(ctx, "bob"), c); err != nil {
			t.Fatal(err)
		}
	}
	if err := cs.Delete(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Create(store.WithActor(ctx, "carol"), &customerv1.Customer{Id: "c1", Name: "D"}); err != nil {
		t.Fatal(err)
	}
	vs, err := cs.Versions(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i, v := range vs {
		got = append(got, fmt.Sprintf("%d:%s:%s", v.Revision, v.Record.GetName(), v.RecordedBy))
		if i < len(vs)-1 && (v.To.IsZero() || v.To.After(vs[i+1].From)) {
			t.Errorf("v%d [%v, %v) overlaps v%d from %v", v.Revision, v.From, v.To, vs[i+1].Revision, vs[i+1].From)
		}
	}
	if strings.Join(got, " ") != "1:A:alice 2:B:bob 3:C:bob 4:D:carol" || !vs[3].To.IsZero() {
		t.Errorf("history: %v", got)
	}
}

// The database itself refuses two versions of a record valid at once,
// whatever code writes them.
func TestOneVersionAtATime(t *testing.T) {
	repos := repotest.New(t)
	ctx := context.Background()
	repos.Customers.Create(ctx, &customerv1.Customer{Id: "c1"})
	db := repos.DB.(*store.PGDB)
	_, err := db.Pool.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (id, revision, tx_from, recorded_by, data) VALUES ('c1', 9, now() - interval '1 day', 'x', '{}')`, db.Qualified(repo.Employer, "customers")))
	if err == nil || !strings.Contains(err.Error(), "customers_one_version_at_a_time") {
		t.Errorf("overlapping version accepted: %v", err)
	}
}

func TestTransactions(t *testing.T) {
	repos := repotest.New(t)
	db := repos.DB
	ctx := context.Background()
	boom := errors.New("boom")
	err := db.InTx(ctx, func(ctx context.Context) error {
		if _, err := repos.Customers.Create(ctx, &customerv1.Customer{Id: "c1"}); err != nil {
			return err
		}
		if _, err := repos.Groups.Create(ctx, &customerv1.Group{Id: "g1", CustomerId: "c1"}); err != nil {
			return err
		}
		if _, err := repos.Customers.Get(ctx, "c1"); err != nil {
			t.Errorf("own write: %v", err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := repos.Customers.Get(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("rolled-back create survived: %v", err)
	}

	// Of two concurrent writes, the second fails with ErrConflict once the
	// first commits.
	c, _ := repos.Customers.Create(ctx, &customerv1.Customer{Id: "c2", Name: "A"})
	result := make(chan error)
	err = db.InTx(ctx, func(tctx context.Context) error {
		mine := proto.Clone(c).(*customerv1.Customer)
		mine.Name = "Mine"
		if _, err := repos.Customers.Update(tctx, mine); err != nil {
			return err
		}
		go func() {
			theirs := proto.Clone(c).(*customerv1.Customer)
			theirs.Name = "Theirs"
			_, err := repos.Customers.Update(ctx, theirs) // waits for our commit
			result <- err
		}()
		time.Sleep(100 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, store.ErrConflict) {
		t.Errorf("losing write: %v", err)
	}
	if got, _ := repos.Customers.Get(ctx, "c2"); got.GetName() != "Mine" {
		t.Errorf("name %s", got.GetName())
	}

	// A context from a finished transaction can't be used again.
	var leaked context.Context
	db.InTx(ctx, func(ctx context.Context) error { leaked = ctx; return nil })
	if _, err := repos.Customers.Create(leaked, &customerv1.Customer{Id: "c3"}); !errors.Is(err, store.ErrTxDone) {
		t.Errorf("write on a finished transaction: %v", err)
	}
}

// Lock makes a second transaction wait, and once the first commits it
// reads what the first wrote.
func TestLockWaits(t *testing.T) {
	repos := repotest.New(t)
	db := repos.DB
	ctx := context.Background()
	repos.Customers.Create(ctx, &customerv1.Customer{Id: "c1"})
	seen := make(chan int)
	err := db.InTx(ctx, func(tctx context.Context) error {
		if err := repos.Customers.Lock(tctx, "c1"); err != nil {
			return err
		}
		go func() {
			_ = db.InTx(ctx, func(ctx context.Context) error {
				if err := repos.Customers.Lock(ctx, "c1"); err != nil {
					return err
				}
				gs, _, err := repos.Groups.List(ctx, store.ListOptions[*customerv1.Group]{Where: []store.Cond{store.Eq("c1", "customerId")}})
				seen <- len(gs)
				return err
			})
		}()
		time.Sleep(100 * time.Millisecond)
		_, err := repos.Groups.Create(tctx, &customerv1.Group{Id: "g1", CustomerId: "c1"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := <-seen; n != 1 {
		t.Errorf("second transaction saw %d groups, want the 1 the first committed", n)
	}
	if err := repos.Customers.Lock(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("lock of a missing record: %v", err)
	}
}

// Where conditions and Go filters, with paging past records the filter
// rejects; old versions never show up.
func TestList(t *testing.T) {
	repos := repotest.New(t)
	ctx := context.Background()
	var all []*claimsv1.Claim
	for i := range 450 {
		c := &claimsv1.Claim{
			Id: fmt.Sprintf("clm_%04d", i), ClaimNumber: fmt.Sprintf("2607%d-E-%05d", i%3, i), MemberId: fmt.Sprintf("mem_%d", i%7),
			Status: claimsv1.ClaimStatus(1 + i%3),
		}
		if i%5 == 0 {
			c.PendOverrides = []*claimsv1.PendOverride{{Code: "X", ResolvedBy: fmt.Sprintf("src_%d", i%2)}}
		}
		saved, err := repos.Claims.Create(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, saved)
	}
	// Move one claim to another member: its old version must not match.
	moved := all[3]
	moved.MemberId = "mem_moved"
	if _, err := repos.Claims.Update(ctx, moved); err != nil {
		t.Fatal(err)
	}
	count := func(q store.ListOptions[*claimsv1.Claim]) int {
		n, token := 0, ""
		for {
			q.PageToken = token
			got, next, err := repos.Claims.List(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			n += len(got)
			if next == "" {
				return n
			}
			token = next
		}
	}
	for name, c := range map[string]struct {
		q    store.ListOptions[*claimsv1.Claim]
		want int
	}{
		"eq":       {store.ListOptions[*claimsv1.Claim]{Where: []store.Cond{store.Eq("mem_3", "memberId")}}, 63},
		"moved":    {store.ListOptions[*claimsv1.Claim]{Where: []store.Cond{store.Eq("mem_moved", "memberId")}}, 1},
		"prefix":   {store.ListOptions[*claimsv1.Claim]{Where: []store.Cond{store.Prefix("26071-", "claimNumber")}}, 150},
		"contains": {store.ListOptions[*claimsv1.Claim]{Where: []store.Cond{store.Contains(`{"pendOverrides":[{"resolvedBy":"src_1"}]}`)}}, 45},
		"enum":     {store.ListOptions[*claimsv1.Claim]{Where: []store.Cond{store.Eq("CLAIM_STATUS_ADJUDICATED", "status")}}, 150},
		"filtered": {store.ListOptions[*claimsv1.Claim]{PageSize: 30, Filter: func(c *claimsv1.Claim) bool { return strings.HasSuffix(c.GetId(), "3") }}, 45},
		"all":      {store.ListOptions[*claimsv1.Claim]{PageSize: 1000}, 450},
	} {
		if got := count(c.q); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}
