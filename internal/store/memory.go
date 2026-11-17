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

// Memory is an in-memory Store that holds each resource as protojson bytes,
// so the prototype exercises the same serialization a real backend would.
// It is safe for concurrent use.
type Memory[T Resource] struct {
	mu   sync.RWMutex
	docs map[string][]byte
	now  func() time.Time
}

// NewMemory returns an empty in-memory store.
func NewMemory[T Resource]() *Memory[T] {
	return &Memory[T]{docs: map[string][]byte{}, now: time.Now}
}

var (
	marshal   = protojson.MarshalOptions{}
	unmarshal = protojson.UnmarshalOptions{}
)

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

func (m *Memory[T]) Get(_ context.Context, id string) (T, error) {
	m.mu.RLock()
	b, ok := m.docs[id]
	m.mu.RUnlock()
	if !ok {
		var zero T
		return zero, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return m.decode(b)
}

func (m *Memory[T]) List(_ context.Context, opts ListOptions[T]) ([]T, string, error) {
	m.mu.RLock()
	ids := make([]string, 0, len(m.docs))
	for id := range m.docs {
		if id > opts.PageToken {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	docs := make([][]byte, len(ids))
	for i, id := range ids {
		docs[i] = m.docs[id]
	}
	m.mu.RUnlock()

	size := opts.PageSize
	if size <= 0 {
		size = defaultPageSize
	}
	var out []T
	for i, b := range docs {
		r, err := m.decode(b)
		if err != nil {
			return nil, "", err
		}
		if opts.Filter != nil && !opts.Filter(r) {
			continue
		}
		out = append(out, r)
		if len(out) == size {
			if i < len(docs)-1 {
				return out, ids[i], nil
			}
			break
		}
	}
	return out, "", nil
}

func (m *Memory[T]) Create(ctx context.Context, r T) (T, error) {
	var zero T
	id := r.GetId()
	if id == "" {
		return zero, fmt.Errorf("%w: id is required", ErrInvalid)
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

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.docs[id]; ok {
		return zero, fmt.Errorf("%w: %s", ErrAlreadyExists, id)
	}
	m.docs[id] = b
	return r, nil
}

func (m *Memory[T]) Update(ctx context.Context, r T) (T, error) {
	var zero T
	id := r.GetId()
	r = proto.Clone(r).(T)

	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.docs[id]
	if !ok {
		return zero, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	cur, err := m.decode(b)
	if err != nil {
		return zero, err
	}
	prev := cur.GetAudit()
	if r.GetAudit().GetRevision() != prev.GetRevision() {
		return zero, fmt.Errorf("%w: %s at revision %d, got %d",
			ErrConflict, id, prev.GetRevision(), r.GetAudit().GetRevision())
	}
	setAudit(r, &commonv1.AuditInfo{
		CreateTime: prev.GetCreateTime(),
		CreatedBy:  prev.GetCreatedBy(),
		UpdateTime: timestamppb.New(m.now()),
		UpdatedBy:  Actor(ctx),
		Revision:   prev.GetRevision() + 1,
	})
	nb, err := marshal.Marshal(r)
	if err != nil {
		return zero, fmt.Errorf("store: encode: %w", err)
	}
	m.docs[id] = nb
	return r, nil
}

func (m *Memory[T]) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.docs[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(m.docs, id)
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
