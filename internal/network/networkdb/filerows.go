package networkdb

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"coreadmin/internal/common/store"
)

// FileRows holds one vendor file's rows in Postgres while its update is
// built: the file is too large to compare in memory, so the comparison
// with what the source states today runs in the database.
//
// It is working data, not a record: the one table that isn't versioned.
// It is dropped once the update is built; what the file changed is kept
// in vendor_provider_changes, and the file itself is the full copy.
type FileRows struct {
	db    *store.PGDB
	name  string // the index's name prefix
	table string // qualified
}

// FileRow is one usable row of the file.
type FileRow struct {
	NPI, TIN  string
	RowNumber int64
	// Hash is a fingerprint of the row's content as sent, whatever its
	// position in the file.
	Hash string
	// JSON is the row as a VendorProviderRow.
	JSON []byte
}

var notIdent = regexp.MustCompile(`[^A-Za-z0-9_]`)

// NewFileRows creates the working table for the file received as jobID.
func NewFileRows(ctx context.Context, db *store.PGDB, jobID string) (*FileRows, error) {
	name := "file_rows_" + notIdent.ReplaceAllString(jobID, "_")
	f := &FileRows{db: db, name: name, table: db.Qualified(Schema, name)}
	_, err := db.Pool.Exec(ctx, fmt.Sprintf(`CREATE UNLOGGED TABLE %s (
		npi text NOT NULL, tin text NOT NULL, row_number bigint NOT NULL, hash text NOT NULL, doc jsonb NOT NULL)`, f.table))
	if err != nil {
		return nil, fmt.Errorf("networkdb: file rows: %w", err)
	}
	return f, nil
}

// Load copies rows in as next yields them (nil at the end), so the file
// is never held in memory, then indexes them by NPI and TIN.
func (f *FileRows) Load(ctx context.Context, next func() (*FileRow, error)) error {
	_, err := f.db.Pool.CopyFrom(ctx, pgx.Identifier{f.db.Schema(Schema), f.name},
		[]string{"npi", "tin", "row_number", "hash", "doc"},
		pgx.CopyFromFunc(func() ([]any, error) {
			r, err := next()
			if err != nil || r == nil {
				return nil, err
			}
			return []any{r.NPI, r.TIN, r.RowNumber, r.Hash, json.RawMessage(r.JSON)}, nil
		}))
	if err != nil {
		return fmt.Errorf("networkdb: load file rows: %w", err)
	}
	for _, q := range []string{
		fmt.Sprintf("CREATE INDEX %q ON %s (npi, tin)", f.name+"_key", f.table),
		"ANALYZE " + f.table,
	} {
		if _, err := f.db.Pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("networkdb: index file rows: %w", err)
		}
	}
	return nil
}

// Difference is one NPI and TIN whose rows in the file differ from what
// the source states today.
type Difference struct {
	NPI, TIN string
	// Hash fingerprints the file's rows for the pair; empty when the file
	// doesn't have the pair.
	Hash string
	// ParticipationID is the source's current participation for the pair;
	// empty when it has none.
	ParticipationID string
	// Rows are the file's rows (VendorProviderRow JSON) in file order;
	// PreviousRows the rows the current participation was derived from.
	Rows, PreviousRows []json.RawMessage
}

// Counts are a file's totals against what the source states today.
type Counts struct {
	// Pairs is the distinct NPI and TIN pairs in the file; Before the
	// participations the source has now.
	Pairs, Before int64
	// NewProviders is the file's NPIs with no provider record; NewTINs its
	// TINs the source has no participation under.
	NewProviders, NewTINs int64
}

// Compare yields every pair that differs between the file and the
// source's current participations, in NPI and TIN order: in the file
// only, in both with different rows, or in the source only. Pairs whose
// rows are the same are not read.
func (f *FileRows) Compare(ctx context.Context, sourceID string, yield func(Difference) error) error {
	parts := f.db.Qualified(Schema, "participations")
	changes := f.db.Qualified(Schema, "vendor_provider_changes")
	rows, err := f.db.Pool.Query(ctx, fmt.Sprintf(`
		WITH f AS (
			SELECT npi, tin, md5(string_agg(hash, '' ORDER BY hash)) AS h FROM %[1]s GROUP BY npi, tin),
		p AS (
			SELECT id, data #>> '{npi}' AS npi, data #>> '{tin}' AS tin, data #>> '{sourceHash}' AS h, data #>> '{vendorChangeId}' AS cid
			FROM %[2]s WHERE tx_to IS NULL AND data #>> '{sourceId}' = $1)
		SELECT coalesce(f.npi, p.npi), coalesce(f.tin, p.tin), coalesce(f.h, ''), coalesce(p.id, ''),
			(SELECT jsonb_agg(s.doc ORDER BY s.row_number) FROM %[1]s s WHERE s.npi = f.npi AND s.tin = f.tin),
			(SELECT c.data -> 'rows' FROM %[3]s c WHERE c.id = p.cid AND c.tx_to IS NULL)
		FROM f FULL JOIN p ON p.npi = f.npi AND p.tin = f.tin
		WHERE f.h IS DISTINCT FROM p.h
		ORDER BY 1, 2`, f.table, parts, changes), sourceID)
	if err != nil {
		return fmt.Errorf("networkdb: compare: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d Difference
		var now, before []byte
		if err := rows.Scan(&d.NPI, &d.TIN, &d.Hash, &d.ParticipationID, &now, &before); err != nil {
			return err
		}
		for _, x := range []struct {
			raw []byte
			to  *[]json.RawMessage
		}{{now, &d.Rows}, {before, &d.PreviousRows}} {
			if len(x.raw) == 0 {
				continue
			}
			if err := json.Unmarshal(x.raw, x.to); err != nil {
				return err
			}
		}
		if err := yield(d); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Counts totals the file against what the source states today.
func (f *FileRows) Counts(ctx context.Context, sourceID string) (Counts, error) {
	parts := f.db.Qualified(Schema, "participations")
	providers := f.db.Qualified(Schema, "providers")
	var c Counts
	err := f.db.Pool.QueryRow(ctx, fmt.Sprintf(`SELECT
		(SELECT count(*) FROM (SELECT 1 FROM %[1]s GROUP BY npi, tin) g),
		(SELECT count(*) FROM %[2]s WHERE tx_to IS NULL AND data #>> '{sourceId}' = $1),
		(SELECT count(*) FROM (SELECT DISTINCT npi FROM %[1]s) n
			WHERE NOT EXISTS (SELECT 1 FROM %[3]s p WHERE p.tx_to IS NULL AND p.data #>> '{npi}' = n.npi)),
		(SELECT count(*) FROM (SELECT DISTINCT tin FROM %[1]s) t
			WHERE NOT EXISTS (SELECT 1 FROM %[2]s p WHERE p.tx_to IS NULL AND p.data #>> '{tin}' = t.tin AND p.data #>> '{sourceId}' = $1))`,
		f.table, parts, providers), sourceID).Scan(&c.Pairs, &c.Before, &c.NewProviders, &c.NewTINs)
	if err != nil {
		return c, fmt.Errorf("networkdb: counts: %w", err)
	}
	return c, nil
}

// Drop removes the working table.
func (f *FileRows) Drop(ctx context.Context) error {
	_, err := f.db.Pool.Exec(ctx, "DROP TABLE IF EXISTS "+f.table)
	return err
}
