package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
)

const defaultPageSize = 100

var (
	marshal   = protojson.MarshalOptions{}
	unmarshal = protojson.UnmarshalOptions{}
)

// PGDB is a set of Postgres schemas, one per domain, holding one versioned
// table per resource type. Nothing is overwritten: every write inserts a row, and the only
// UPDATE closes the version it replaces. Every table has the same shape:
//
//	id           text         the record
//	revision     bigint       1, 2, 3... (audit.revision)
//	tx_from      timestamptz  when this version became the valid one
//	tx_to        timestamptz  when it stopped being valid; NULL while current
//	recorded_by  text         who wrote it
//	data         jsonb        the record, as protojson
//
// with UNIQUE (id, revision) and an exclusion constraint that a record's
// versions never overlap in time: at any moment, past or present, exactly
// one version of a record is valid. Reads see current versions (tx_to IS
// NULL); Versions reads the history. Indexes for access patterns are on
// current versions only.
//
// It implements Transactor with Postgres transactions at READ COMMITTED.
// A write names the revision it read, so of two concurrent writes the
// second fails with ErrConflict; Lock is SELECT ... FOR UPDATE.
type PGDB struct {
	Pool *pgxpool.Pool
	// Prefix names a sandbox: its schemas are <prefix>_<domain>. Empty in
	// production, where each domain's schema is just its name.
	Prefix string
	// Now is the clock for transaction time. Defaults to time.Now; the
	// sandboxes set their own, so demos are repeatable.
	Now func() time.Time
}

// Schema is the name of a domain's schema in this database.
func (db *PGDB) Schema(domain string) string {
	if db.Prefix == "" {
		return domain
	}
	return db.Prefix + "_" + domain
}

func (db *PGDB) now() time.Time {
	if db.Now != nil {
		return db.Now()
	}
	return time.Now()
}

// Table is one resource type's table and the indexes its access patterns
// need.
type Table struct {
	// Schema is the domain that owns the table.
	Schema  string
	Name    string
	Indexes []Index
}

// Index is an index on a table's current versions, and why it exists.
type Index struct {
	Name string
	// The lookup it serves, which would otherwise read the whole table.
	Why string
	// A field path (an index on data #>> path, serving Eq and Prefix), or
	// GIN (a jsonb_path_ops index on data, serving Contains).
	Path []string
	GIN  bool
}

var ident = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Qualified is a table's name in SQL: "schema"."table".
func (db *PGDB) Qualified(domain, table string) string {
	return fmt.Sprintf("%q.%q", db.Schema(domain), table)
}

// Migrate creates the tables' schemas, the tables and their indexes, if
// missing.
func (db *PGDB) Migrate(ctx context.Context, tables []Table) error {
	stmts := []string{
		"CREATE EXTENSION IF NOT EXISTS btree_gist", // = on text inside an exclusion constraint
	}
	made := map[string]bool{}
	for _, t := range tables {
		schema := db.Schema(t.Schema)
		if !ident.MatchString(schema) || !ident.MatchString(t.Name) {
			return fmt.Errorf("%w: table %q.%q", ErrInvalid, schema, t.Name)
		}
		if !made[schema] {
			made[schema] = true
			stmts = append(stmts, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %q", schema))
		}
		q := db.Qualified(t.Schema, t.Name)
		stmts = append(stmts,
			fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
				id text NOT NULL,
				revision bigint NOT NULL,
				tx_from timestamptz NOT NULL,
				tx_to timestamptz,
				recorded_by text NOT NULL,
				data jsonb NOT NULL,
				PRIMARY KEY (id, revision),
				CHECK (tx_to IS NULL OR tx_to >= tx_from),
				CONSTRAINT %q EXCLUDE USING gist (id WITH =, tstzrange(tx_from, tx_to) WITH &&))`, q, t.Name+"_one_version_at_a_time"),
			// Current versions by id, and a record's history in order.
			fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %q ON %s (id) WHERE tx_to IS NULL", t.Name+"_current", q),
			fmt.Sprintf("CREATE INDEX IF NOT EXISTS %q ON %s (id, tx_from)", t.Name+"_history", q),
		)
		for _, ix := range t.Indexes {
			if !ident.MatchString(ix.Name) {
				return fmt.Errorf("%w: index %q", ErrInvalid, ix.Name)
			}
			if ix.GIN {
				stmts = append(stmts, fmt.Sprintf("CREATE INDEX IF NOT EXISTS %q ON %s USING gin (data jsonb_path_ops) WHERE tx_to IS NULL", ix.Name, q))
				continue
			}
			expr, err := pathExpr(ix.Path)
			if err != nil {
				return err
			}
			// text_pattern_ops serves both equality and prefix (LIKE 'x%').
			stmts = append(stmts, fmt.Sprintf("CREATE INDEX IF NOT EXISTS %q ON %s ((%s) text_pattern_ops) WHERE tx_to IS NULL", ix.Name, q, expr))
		}
	}
	for _, s := range stmts {
		if _, err := db.Pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
	}
	return nil
}

// Drop drops a sandbox's schemas for the given domains, and everything in
// them. It refuses without a prefix: production schemas are never dropped.
func (db *PGDB) Drop(ctx context.Context, domains ...string) error {
	if db.Prefix == "" {
		return fmt.Errorf("%w: drop without a sandbox prefix", ErrInvalid)
	}
	for _, d := range domains {
		if err := DropSchema(ctx, db.Pool, db.Schema(d)); err != nil {
			return err
		}
	}
	return nil
}

// DropSchema drops one schema and everything in it.
func DropSchema(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	if !ident.MatchString(schema) {
		return fmt.Errorf("%w: schema %q", ErrInvalid, schema)
	}
	_, err := pool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %q CASCADE", schema))
	return err
}

// pathExpr is the SQL for the text at a protojson path: data #>> '{a,b}'.
// Indexes and queries build it the same way, so they match.
func pathExpr(path []string) (string, error) {
	if len(path) == 0 {
		return "", fmt.Errorf("%w: empty path", ErrInvalid)
	}
	for _, p := range path {
		if !ident.MatchString(p) {
			return "", fmt.Errorf("%w: path element %q", ErrInvalid, p)
		}
	}
	return fmt.Sprintf("(data #>> '{%s}')", strings.Join(path, ",")), nil
}

// ---------- transactions ----------

type pgTxKey struct{}

type pgTx struct {
	db   *PGDB
	tx   pgx.Tx
	done bool
}

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (db *PGDB) InTx(ctx context.Context, f func(ctx context.Context) error) error {
	if t, ok := ctx.Value(pgTxKey{}).(*pgTx); ok && t.db == db {
		return f(ctx)
	}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	t := &pgTx{db: db, tx: tx}
	err = f(context.WithValue(ctx, pgTxKey{}, t))
	t.done = true
	if err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

// q is the transaction ctx carries, or the pool.
func (db *PGDB) q(ctx context.Context) (querier, bool, error) {
	if t, ok := ctx.Value(pgTxKey{}).(*pgTx); ok && t.db == db {
		if t.done {
			return nil, false, ErrTxDone
		}
		return t.tx, true, nil
	}
	return db.Pool, false, nil
}

// mapErr turns Postgres's concurrency and uniqueness failures into the
// store's errors, so callers retry them like any other conflict.
func mapErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "40001", "40P01": // serialization failure, deadlock
			return fmt.Errorf("%w: %s", ErrConflict, pg.Message)
		case "23505", "23P01": // unique violation, exclusion violation
			return fmt.Errorf("%w: %s", ErrConflict, pg.Message)
		}
	}
	return err
}

// ---------- the store ----------

// PG is the Store for one resource type.
type PG[T Resource] struct {
	db    *PGDB
	table string
}

// NewPG is the store for a table in a domain's schema.
func NewPG[T Resource](db *PGDB, domain, table string) *PG[T] {
	return &PG[T]{db: db, table: db.Qualified(domain, table)}
}

func (s *PG[T]) newT() T {
	var zero T
	return zero.ProtoReflect().Type().New().Interface().(T)
}

func (s *PG[T]) decode(b []byte) (T, error) {
	r := s.newT()
	if err := unmarshal.Unmarshal(b, r); err != nil {
		var zero T
		return zero, fmt.Errorf("store: decode: %w", err)
	}
	return r, nil
}

func (s *PG[T]) Get(ctx context.Context, id string) (T, error) {
	var zero T
	q, _, err := s.db.q(ctx)
	if err != nil {
		return zero, err
	}
	var b []byte
	err = q.QueryRow(ctx, "SELECT data FROM "+s.table+" WHERE id = $1 AND tx_to IS NULL", id).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return zero, mapErr(err)
	}
	return s.decode(b)
}

// listSQL is the query List runs for opts, its arguments, and the batch
// size it reads at a time.
func (s *PG[T]) listSQL(opts ListOptions[T]) (string, []any, int, error) {
	where := []string{"tx_to IS NULL", "id > $1"}
	args := []any{opts.PageToken}
	for _, c := range opts.Where {
		args = append(args, c.Value)
		n := len(args)
		switch c.Op {
		case OpEq, OpPrefix:
			expr, err := pathExpr(c.Path)
			if err != nil {
				return "", nil, 0, err
			}
			if c.Op == OpEq {
				where = append(where, fmt.Sprintf("%s = $%d", expr, n))
			} else {
				args[n-1] = likeEscape(c.Value) + "%"
				where = append(where, fmt.Sprintf("%s LIKE $%d", expr, n))
			}
		case OpContains:
			where = append(where, fmt.Sprintf("data @> $%d::jsonb", n))
		default:
			return "", nil, 0, fmt.Errorf("%w: condition op %d", ErrInvalid, c.Op)
		}
	}
	size := opts.PageSize
	if size <= 0 {
		size = defaultPageSize
	}
	// Filter runs in Go, so read in batches until the page is full.
	batch := max(size, 200)
	return fmt.Sprintf("SELECT id, data FROM %s WHERE %s ORDER BY id LIMIT %d", s.table, strings.Join(where, " AND "), batch+1), args, batch, nil
}

// Explain is Postgres's plan for the query List would run for opts: it
// shows whether an index serves it. The plan depends on the data and its
// statistics, so explain against realistic data.
func (s *PG[T]) Explain(ctx context.Context, opts ListOptions[T]) (string, error) {
	sql, args, _, err := s.listSQL(opts)
	if err != nil {
		return "", err
	}
	var plan []string
	err = s.db.InTx(ctx, func(ctx context.Context) error {
		q, _, err := s.db.q(ctx)
		if err != nil {
			return err
		}
		rows, err := q.Query(ctx, "EXPLAIN "+sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan = append(plan, line)
		}
		return rows.Err()
	})
	return strings.Join(plan, "\n"), err
}

func (s *PG[T]) List(ctx context.Context, opts ListOptions[T]) ([]T, string, error) {
	q, _, err := s.db.q(ctx)
	if err != nil {
		return nil, "", err
	}
	sql, args, batch, err := s.listSQL(opts)
	if err != nil {
		return nil, "", err
	}
	size := opts.PageSize
	if size <= 0 {
		size = defaultPageSize
	}
	var out []T
	for {
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return nil, "", mapErr(err)
		}
		type row struct {
			id   string
			data []byte
		}
		var got []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.data); err != nil {
				rows.Close()
				return nil, "", err
			}
			got = append(got, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, "", mapErr(err)
		}
		more := len(got) > batch
		if more {
			got = got[:batch]
		}
		for i, r := range got {
			v, err := s.decode(r.data)
			if err != nil {
				return nil, "", err
			}
			if opts.Filter != nil && !opts.Filter(v) {
				continue
			}
			out = append(out, v)
			if len(out) == size {
				if i < len(got)-1 || more {
					return out, r.id, nil
				}
				return out, "", nil
			}
		}
		if !more {
			return out, "", nil
		}
		args[0] = got[len(got)-1].id
	}
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// insert writes one version. ON CONFLICT DO NOTHING keeps a collision with
// another writer from aborting the transaction; zero rows means someone
// else wrote that version first.
func (s *PG[T]) insert(ctx context.Context, q querier, r T, rev int64, from time.Time) (bool, error) {
	b, err := marshal.Marshal(r)
	if err != nil {
		return false, fmt.Errorf("store: encode: %w", err)
	}
	tag, err := q.Exec(ctx, "INSERT INTO "+s.table+" (id, revision, tx_from, recorded_by, data) VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING",
		r.GetId(), rev, from, Actor(ctx), b)
	if err != nil {
		return false, mapErr(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PG[T]) Create(ctx context.Context, r T) (T, error) {
	var zero T
	if r.GetId() == "" {
		return zero, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	q, _, err := s.db.q(ctx)
	if err != nil {
		return zero, err
	}
	// A record that was deleted can be created again: its history
	// continues with the next revision.
	var current bool
	var last int64
	var ended *time.Time
	err = q.QueryRow(ctx, "SELECT coalesce(bool_or(tx_to IS NULL), false), coalesce(max(revision), 0), max(tx_to) FROM "+s.table+" WHERE id = $1",
		r.GetId()).Scan(&current, &last, &ended)
	if err != nil {
		return zero, mapErr(err)
	}
	if current {
		return zero, fmt.Errorf("%w: %s", ErrAlreadyExists, r.GetId())
	}
	r = proto.Clone(r).(T)
	now := s.db.now()
	if ended != nil && now.Before(*ended) {
		now = *ended
	}
	ts := timestamppb.New(now)
	actor := Actor(ctx)
	setAudit(r, &commonv1.AuditInfo{CreateTime: ts, UpdateTime: ts, CreatedBy: actor, UpdatedBy: actor, Revision: last + 1})
	ok, err := s.insert(ctx, q, r, last+1, now)
	if err != nil {
		return zero, err
	}
	if !ok {
		return zero, fmt.Errorf("%w: %s", ErrAlreadyExists, r.GetId())
	}
	return r, nil
}

// close ends the current version, if it is still the one at revision rev:
// the one UPDATE the store ever issues.
func (s *PG[T]) close(ctx context.Context, q querier, id string, rev int64, at time.Time) (bool, error) {
	tag, err := q.Exec(ctx, "UPDATE "+s.table+" SET tx_to = $3 WHERE id = $1 AND revision = $2 AND tx_to IS NULL", id, rev, at)
	if err != nil {
		return false, mapErr(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PG[T]) Update(ctx context.Context, r T) (T, error) {
	var zero T
	q, _, err := s.db.q(ctx)
	if err != nil {
		return zero, err
	}
	id := r.GetId()
	var cur []byte
	var rev int64
	var from time.Time
	err = q.QueryRow(ctx, "SELECT data, revision, tx_from FROM "+s.table+" WHERE id = $1 AND tx_to IS NULL", id).Scan(&cur, &rev, &from)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return zero, mapErr(err)
	}
	if r.GetAudit().GetRevision() != rev {
		return zero, fmt.Errorf("%w: %s at revision %d, got %d", ErrConflict, id, rev, r.GetAudit().GetRevision())
	}
	prev, err := s.decode(cur)
	if err != nil {
		return zero, err
	}
	now := s.db.now()
	if now.Before(from) {
		// A clock behind the last write (a sandbox's) still moves forward.
		now = from
	}
	r = proto.Clone(r).(T)
	setAudit(r, &commonv1.AuditInfo{
		CreateTime: prev.GetAudit().GetCreateTime(), CreatedBy: prev.GetAudit().GetCreatedBy(),
		UpdateTime: timestamppb.New(now), UpdatedBy: Actor(ctx), Revision: rev + 1,
	})
	// Close the version we read, then insert the next. If another writer
	// got there first, the close finds nothing: a conflict.
	if ok, err := s.close(ctx, q, id, rev, now); err != nil {
		return zero, err
	} else if !ok {
		return zero, fmt.Errorf("%w: %s changed while being updated", ErrConflict, id)
	}
	if ok, err := s.insert(ctx, q, r, rev+1, now); err != nil {
		return zero, err
	} else if !ok {
		return zero, fmt.Errorf("%w: %s changed while being updated", ErrConflict, id)
	}
	return r, nil
}

// Delete ends the record's current version without a successor. Its
// history stays.
func (s *PG[T]) Delete(ctx context.Context, id string) error {
	q, _, err := s.db.q(ctx)
	if err != nil {
		return err
	}
	var rev int64
	var from time.Time
	err = q.QueryRow(ctx, "SELECT revision, tx_from FROM "+s.table+" WHERE id = $1 AND tx_to IS NULL", id).Scan(&rev, &from)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return mapErr(err)
	}
	now := s.db.now()
	if now.Before(from) {
		now = from
	}
	if ok, err := s.close(ctx, q, id, rev, now); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s changed while being deleted", ErrConflict, id)
	}
	return nil
}

func (s *PG[T]) Lock(ctx context.Context, id string) error {
	q, inTx, err := s.db.q(ctx)
	if err != nil {
		return err
	}
	sql := "SELECT 1 FROM " + s.table + " WHERE id = $1 AND tx_to IS NULL"
	if inTx {
		sql += " FOR UPDATE"
	}
	var one int
	err = q.QueryRow(ctx, sql, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return mapErr(err)
}

// Versions is a record's history, oldest first: every version, including
// the current one, with when it was valid and who wrote it.
func (s *PG[T]) Versions(ctx context.Context, id string) ([]Version[T], error) {
	q, _, err := s.db.q(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, "SELECT data, revision, tx_from, tx_to, recorded_by FROM "+s.table+" WHERE id = $1 ORDER BY revision", id)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []Version[T]
	for rows.Next() {
		var b []byte
		var v Version[T]
		var to *time.Time
		if err := rows.Scan(&b, &v.Revision, &v.From, &to, &v.RecordedBy); err != nil {
			return nil, err
		}
		if to != nil {
			v.To = *to
		}
		if v.Record, err = s.decode(b); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
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
