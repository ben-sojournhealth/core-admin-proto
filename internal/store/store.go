// Package store defines persistence for core admin resources.
//
// Every top-level resource is a protobuf message with an `id` and an
// `audit` field, persisted as protojson. Store is the interface services
// program against; PG (pg.go) implements it on Postgres, versioned and
// insert-only: nothing is overwritten, so every table is its own audit
// log.
package store

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
)

var (
	ErrNotFound      = errors.New("store: not found")
	ErrAlreadyExists = errors.New("store: already exists")
	// ErrConflict is returned by Update when the caller's audit.revision
	// does not match the stored revision (someone else wrote first).
	ErrConflict = errors.New("store: revision conflict")
	ErrInvalid  = errors.New("store: invalid resource")
	// ErrTxDone is returned when a context from a finished transaction is
	// used again.
	ErrTxDone = errors.New("store: transaction already finished")
)

// Transactor runs work in a transaction.
//
// InTx calls f with a context that carries the transaction. Every read and
// write made with that context, on any store from the same database, is
// part of it: reads see the transaction's own writes, and no one else sees
// them until it commits. If f returns an error nothing it wrote is kept;
// otherwise everything commits together. Commit fails with ErrConflict
// (or ErrAlreadyExists) if another transaction changed or created a record
// this one wrote; the caller retries from the start, re-reading.
//
// Reads are read-committed: each sees the latest committed version, not a
// snapshot from the transaction's start. Revisions guard the writes.
//
// InTx inside f joins the transaction already in progress.
type Transactor interface {
	InTx(ctx context.Context, f func(ctx context.Context) error) error
}

// Resource is satisfied by every generated top-level message
// (Customer, Plan, Member, Claim, ...).
type Resource interface {
	proto.Message
	GetId() string
	GetAudit() *commonv1.AuditInfo
}

// ListOptions controls List. Results are ordered by id. Where narrows
// the records a store reads (a database answers it from an index); Filter
// then runs in Go on each record that passed. Use Where for the
// conditions an access pattern depends on, and Filter for the rest.
type ListOptions[T Resource] struct {
	Where     []Cond
	Filter    func(T) bool
	PageSize  int
	PageToken string
}

// Op is how a Cond compares.
type Op int

const (
	// OpEq: the field at Path equals Value.
	OpEq Op = iota + 1
	// OpPrefix: the field at Path starts with Value.
	OpPrefix
	// OpContains: the record contains the JSON document Value, as
	// Postgres's @> (every key and array element in Value is present).
	OpContains
)

// Cond is a condition on a stored record, addressed by the record's
// protojson field names (lowerCamelCase, as stored): e.g. Path
// {"sourceRecord", "ingestionJobId"}. Enum fields compare by value name,
// int64 fields by their decimal string.
type Cond struct {
	Path  []string
	Op    Op
	Value string
}

// Eq is the condition that the field at path equals value.
func Eq(value string, path ...string) Cond { return Cond{Path: path, Op: OpEq, Value: value} }

// Prefix is the condition that the field at path starts with prefix.
func Prefix(prefix string, path ...string) Cond { return Cond{Path: path, Op: OpPrefix, Value: prefix} }

// Contains is the condition that the record contains doc, a JSON object:
// e.g. {"claims":[{"claimId":"x"}]} for a record whose claims list has an
// entry with that claimId.
func Contains(doc string) Cond { return Cond{Op: OpContains, Value: doc} }

// Store persists one resource type.
//
// Implementations return copies: mutating a returned message never changes
// stored state. Create and Update stamp `audit` (times, actor, revision)
// and return the stored version.
type Store[T Resource] interface {
	Get(ctx context.Context, id string) (T, error)
	List(ctx context.Context, opts ListOptions[T]) (items []T, nextPageToken string, err error)
	Create(ctx context.Context, r T) (T, error)
	// Update replaces the resource. r.audit.revision must equal the stored
	// revision, otherwise ErrConflict.
	Update(ctx context.Context, r T) (T, error)
	Delete(ctx context.Context, id string) error
	// Lock claims a record for the rest of the transaction without
	// changing it: the transaction then conflicts with any other that
	// writes or locks the record before it commits. Take the lock before
	// reading whatever a decision depends on, so two transactions can't
	// both act on the same view of it. It is Postgres's SELECT ... FOR
	// UPDATE (which waits rather than failing). Outside a transaction it
	// only checks that the record exists.
	Lock(ctx context.Context, id string) error
	// Versions is the record's history, oldest first, the current version
	// last. A deleted record's history remains.
	Versions(ctx context.Context, id string) ([]Version[T], error)
}

// Version is one version of a record: what it was, and when it was the
// valid one.
type Version[T Resource] struct {
	Record   T
	Revision int64
	// From when it was valid; To when it stopped (zero while current).
	From, To   time.Time
	RecordedBy string
}

type actorKey struct{}

// WithActor records who is making a change; stores write it into
// audit.created_by / audit.updated_by.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// Actor returns the actor set by WithActor, or "system".
func Actor(ctx context.Context) string {
	if a, ok := ctx.Value(actorKey{}).(string); ok && a != "" {
		return a
	}
	return "system"
}
