// Package store defines persistence for core admin resources.
//
// Every top-level resource is a protobuf message with an `id` and an
// `audit` field, persisted as protojson. Store is the interface services
// program against; Memory is the in-process implementation used for the
// prototype and tests. A real backend (document DB, Postgres JSONB, object
// storage) implements the same interface.
package store

import (
	"context"
	"errors"

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
)

// Resource is satisfied by every generated top-level message
// (Customer, Plan, Member, Claim, ...).
type Resource interface {
	proto.Message
	GetId() string
	GetAudit() *commonv1.AuditInfo
}

// ListOptions controls List. Filter runs against each resource; results
// are ordered by id.
type ListOptions[T Resource] struct {
	Filter    func(T) bool
	PageSize  int
	PageToken string
}

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
