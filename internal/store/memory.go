package store

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
)

const defaultPageSize = 100

// MemoryDB groups Memory stores into one database, so a transaction can
// span them. It implements Transactor.
type MemoryDB struct {
	// commit serializes commits: one transaction validates and applies its
	// writes at a time.
	commit sync.Mutex
}

func NewMemoryDB() *MemoryDB { return &MemoryDB{} }

// Memory is an in-memory Store that holds each resource as protojson bytes,
// so the prototype exercises the same serialization a real backend would.
// It is safe for concurrent use.
type Memory[T Resource] struct {
	db   *MemoryDB
	mu   sync.RWMutex
	rows map[string]row
	now  func() time.Time
}

// row is one committed record. gen counts every commit that wrote or
// locked it; transactions are checked against it.
type row struct {
	data []byte
	rev  int64
	gen  int64
}

// NewMemory returns an empty in-memory store in a database of its own.
func NewMemory[T Resource]() *Memory[T] { return NewMemoryIn[T](NewMemoryDB()) }

// NewMemoryIn returns an empty in-memory store in db.
func NewMemoryIn[T Resource](db *MemoryDB) *Memory[T] {
	return &Memory[T]{db: db, rows: map[string]row{}, now: time.Now}
}

var (
	marshal   = protojson.MarshalOptions{}
	unmarshal = protojson.UnmarshalOptions{}
)

// ---------- transactions ----------

type txKey struct{}

// memTx buffers a transaction's writes until commit.
type memTx struct {
	db     *MemoryDB
	mu     sync.Mutex
	writes map[table]map[string]*write
	done   bool
}

// write is a pending change to one record. data is nil for a delete.
// lock means the record is only locked, not changed. create means the
// record did not exist when the transaction first touched it; otherwise
// base is the committed gen it was read at.
type write struct {
	data   []byte
	rev    int64
	lock   bool
	create bool
	base   int64
}

// table is what a commit needs from each store it wrote to.
type table interface {
	lock()
	unlock()
	check(id string, w *write) error
	apply(id string, w *write)
}

func (db *MemoryDB) InTx(ctx context.Context, f func(ctx context.Context) error) error {
	if tx, ok := ctx.Value(txKey{}).(*memTx); ok && tx.db == db {
		return f(ctx)
	}
	tx := &memTx{db: db, writes: map[table]map[string]*write{}}
	err := f(context.WithValue(ctx, txKey{}, tx))
	tx.mu.Lock()
	defer tx.mu.Unlock()
	tx.done = true
	if err != nil {
		return err
	}
	return tx.commitLocked()
}

// commitLocked checks every write against what is committed now, then
// applies all of them, holding every table it touches so no one sees half.
func (tx *memTx) commitLocked() error {
	if len(tx.writes) == 0 {
		return nil
	}
	tx.db.commit.Lock()
	defer tx.db.commit.Unlock()
	for t := range tx.writes {
		t.lock()
		defer t.unlock()
	}
	for t, ws := range tx.writes {
		for id, w := range ws {
			if err := t.check(id, w); err != nil {
				return err
			}
		}
	}
	for t, ws := range tx.writes {
		for id, w := range ws {
			t.apply(id, w)
		}
	}
	return nil
}

func (m *Memory[T]) lock()   { m.mu.Lock() }
func (m *Memory[T]) unlock() { m.mu.Unlock() }

func (m *Memory[T]) check(id string, w *write) error {
	cur, ok := m.rows[id]
	switch {
	case w.create && ok:
		return fmt.Errorf("%w: %s", ErrAlreadyExists, id)
	case w.create:
		return nil
	case !ok:
		return fmt.Errorf("%w: %s was deleted", ErrConflict, id)
	case cur.gen != w.base:
		return fmt.Errorf("%w: %s was changed or locked by another transaction", ErrConflict, id)
	}
	return nil
}

func (m *Memory[T]) apply(id string, w *write) {
	cur := m.rows[id]
	switch {
	case w.lock:
		cur.gen++
		m.rows[id] = cur
	case w.data == nil:
		delete(m.rows, id)
	default:
		m.rows[id] = row{data: w.data, rev: w.rev, gen: cur.gen + 1}
	}
}

// tx returns the transaction ctx carries for this store's database.
func (m *Memory[T]) tx(ctx context.Context) (*memTx, error) {
	tx, ok := ctx.Value(txKey{}).(*memTx)
	if !ok || tx.db != m.db {
		return nil, nil
	}
	if tx.done {
		return nil, ErrTxDone
	}
	return tx, nil
}

// pending is the transaction's write to id, if any. Caller holds tx.mu.
func (m *Memory[T]) pending(tx *memTx, id string) *write {
	return tx.writes[m][id]
}

func (m *Memory[T]) stage(tx *memTx, id string, w *write) {
	if tx.writes[m] == nil {
		tx.writes[m] = map[string]*write{}
	}
	tx.writes[m][id] = w
}

// ---------- reads ----------

func (m *Memory[T]) newT() T {
	var zero T
	return zero.ProtoReflect().Type().New().Interface().(T)
}

func (m *Memory[T]) decode(b []byte) (T, error) {
	r := m.newT()
	if err := unmarshal.Unmarshal(b, r); err != nil {
		var zero T
		return zero, fmt.Errorf("store: decode: %w", err)
	}
	return r, nil
}

func (m *Memory[T]) committed(id string) (row, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.rows[id]
	return r, ok
}

// visible is the record as ctx sees it: the transaction's own write if it
// made one, otherwise the committed version.
func (m *Memory[T]) visible(tx *memTx, id string) (row, bool) {
	if tx != nil {
		if w := m.pending(tx, id); w != nil && !w.lock {
			return row{data: w.data, rev: w.rev}, w.data != nil
		}
	}
	return m.committed(id)
}

func (m *Memory[T]) Get(ctx context.Context, id string) (T, error) {
	var zero T
	tx, err := m.tx(ctx)
	if err != nil {
		return zero, err
	}
	if tx != nil {
		tx.mu.Lock()
		defer tx.mu.Unlock()
	}
	r, ok := m.visible(tx, id)
	if !ok {
		return zero, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return m.decode(r.data)
}

func (m *Memory[T]) List(ctx context.Context, opts ListOptions[T]) ([]T, string, error) {
	tx, err := m.tx(ctx)
	if err != nil {
		return nil, "", err
	}
	m.mu.RLock()
	rows := make(map[string][]byte, len(m.rows))
	for id, r := range m.rows {
		if id > opts.PageToken {
			rows[id] = r.data
		}
	}
	m.mu.RUnlock()
	if tx != nil {
		tx.mu.Lock()
		for id, w := range tx.writes[m] {
			switch {
			case id <= opts.PageToken || w.lock:
			case w.data == nil:
				delete(rows, id)
			default:
				rows[id] = w.data
			}
		}
		tx.mu.Unlock()
	}
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	size := opts.PageSize
	if size <= 0 {
		size = defaultPageSize
	}
	var out []T
	for i, id := range ids {
		r, err := m.decode(rows[id])
		if err != nil {
			return nil, "", err
		}
		if opts.Filter != nil && !opts.Filter(r) {
			continue
		}
		out = append(out, r)
		if len(out) == size {
			if i < len(ids)-1 {
				return out, ids[i], nil
			}
			break
		}
	}
	return out, "", nil
}

// ---------- writes ----------

func (m *Memory[T]) Create(ctx context.Context, r T) (T, error) {
	var zero T
	id := r.GetId()
	if id == "" {
		return zero, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	tx, err := m.tx(ctx)
	if err != nil {
		return zero, err
	}
	r = proto.Clone(r).(T)
	now := timestamppb.New(m.now())
	actor := Actor(ctx)
	setAudit(r, &commonv1.AuditInfo{
		CreateTime: now, UpdateTime: now,
		CreatedBy: actor, UpdatedBy: actor,
		Revision: 1,
	})
	b, err := marshal.Marshal(r)
	if err != nil {
		return zero, fmt.Errorf("store: encode: %w", err)
	}

	if tx == nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.rows[id]; ok {
			return zero, fmt.Errorf("%w: %s", ErrAlreadyExists, id)
		}
		m.rows[id] = row{data: b, rev: 1, gen: 1}
		return r, nil
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if _, ok := m.visible(tx, id); ok {
		return zero, fmt.Errorf("%w: %s", ErrAlreadyExists, id)
	}
	w := &write{data: b, rev: 1, create: true}
	if prev := m.pending(tx, id); prev != nil {
		// Deleted earlier in this transaction: still checked against
		// what that delete was based on.
		w.create, w.base = prev.create, prev.base
	}
	m.stage(tx, id, w)
	return r, nil
}

func (m *Memory[T]) Update(ctx context.Context, r T) (T, error) {
	var zero T
	id := r.GetId()
	tx, err := m.tx(ctx)
	if err != nil {
		return zero, err
	}
	r = proto.Clone(r).(T)

	if tx == nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		cur, ok := m.rows[id]
		if !ok {
			return zero, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		b, err := m.revise(ctx, r, cur)
		if err != nil {
			return zero, err
		}
		m.rows[id] = row{data: b, rev: cur.rev + 1, gen: cur.gen + 1}
		return r, nil
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	cur, ok := m.visible(tx, id)
	if !ok {
		return zero, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	b, err := m.revise(ctx, r, cur)
	if err != nil {
		return zero, err
	}
	w := &write{data: b, rev: cur.rev + 1, base: cur.gen}
	if prev := m.pending(tx, id); prev != nil {
		w.create, w.base = prev.create, prev.base
	}
	m.stage(tx, id, w)
	return r, nil
}

// revise checks r against the current version, stamps its audit for the
// next revision and encodes it.
func (m *Memory[T]) revise(ctx context.Context, r T, cur row) ([]byte, error) {
	prev, err := m.decode(cur.data)
	if err != nil {
		return nil, err
	}
	if r.GetAudit().GetRevision() != cur.rev {
		return nil, fmt.Errorf("%w: %s at revision %d, got %d", ErrConflict, r.GetId(), cur.rev, r.GetAudit().GetRevision())
	}
	setAudit(r, &commonv1.AuditInfo{
		CreateTime: prev.GetAudit().GetCreateTime(),
		CreatedBy:  prev.GetAudit().GetCreatedBy(),
		UpdateTime: timestamppb.New(m.now()),
		UpdatedBy:  Actor(ctx),
		Revision:   cur.rev + 1,
	})
	b, err := marshal.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("store: encode: %w", err)
	}
	return b, nil
}

func (m *Memory[T]) Delete(ctx context.Context, id string) error {
	tx, err := m.tx(ctx)
	if err != nil {
		return err
	}
	if tx == nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.rows[id]; !ok {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		delete(m.rows, id)
		return nil
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	cur, ok := m.visible(tx, id)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	prev := m.pending(tx, id)
	switch {
	case prev != nil && prev.create:
		// Created and deleted in this transaction: nothing to commit.
		delete(tx.writes[m], id)
	case prev != nil:
		m.stage(tx, id, &write{base: prev.base})
	default:
		m.stage(tx, id, &write{base: cur.gen})
	}
	return nil
}

func (m *Memory[T]) Lock(ctx context.Context, id string) error {
	tx, err := m.tx(ctx)
	if err != nil {
		return err
	}
	if tx == nil {
		if _, ok := m.committed(id); !ok {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if _, ok := m.visible(tx, id); !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if m.pending(tx, id) != nil {
		// Already written or locked by this transaction.
		return nil
	}
	cur, _ := m.committed(id)
	m.stage(tx, id, &write{lock: true, base: cur.gen})
	return nil
}

// setAudit writes the `audit` field via reflection; generated messages have
// no setters.
func setAudit(r Resource, a *commonv1.AuditInfo) {
	msg := r.ProtoReflect()
	fd := msg.Descriptor().Fields().ByName("audit")
	if fd == nil {
		panic(fmt.Sprintf("store: %s has no audit field", msg.Descriptor().FullName()))
	}
	msg.Set(fd, protoreflect.ValueOfMessage(a.ProtoReflect()))
}
