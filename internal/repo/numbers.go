package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"coreadmin/internal/store"
)

// ClaimNumbers reserves claim numbers.
type ClaimNumbers interface {
	// Reserve takes the next count numbers under prefix (YYJJJ-S-) and
	// returns the first. ref says what they're for, e.g. a file name.
	Reserve(ctx context.Context, prefix string, count int, ref string) (int, error)
}

// pgClaimNumbers keeps reservations in claim_number_blocks: one row per
// block, inserted and never changed, so the table is the record of every
// number handed out and why. Blocks under a prefix never overlap: the
// database refuses it.
//
// A reservation commits on its own, outside any transaction the caller
// is in: a number is spent even if what it was for rolls back, like a
// sequence. The block's ref and the claims that use it explain any gap.
type pgClaimNumbers struct{ db *store.PGDB }

func (n *pgClaimNumbers) table() string { return fmt.Sprintf("%q.claim_number_blocks", n.db.Schema) }

func (n *pgClaimNumbers) migrate(ctx context.Context) error {
	_, err := n.db.Pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		prefix text NOT NULL,
		first_number int NOT NULL,
		last_number int NOT NULL,
		ref text NOT NULL,
		reserved_at timestamptz NOT NULL DEFAULT now(),
		reserved_by text NOT NULL,
		PRIMARY KEY (prefix, first_number),
		CHECK (last_number >= first_number),
		CONSTRAINT claim_number_blocks_never_overlap EXCLUDE USING gist (prefix WITH =, int4range(first_number, last_number, '[]') WITH &&))`, n.table()))
	return err
}

func (n *pgClaimNumbers) Reserve(ctx context.Context, prefix string, count int, ref string) (int, error) {
	if count < 1 {
		return 0, fmt.Errorf("%w: reserve %d numbers", store.ErrInvalid, count)
	}
	// Two reservations racing compute the same next block; the database
	// refuses the second, which tries again from the new end.
	for range 20 {
		var first int
		err := n.db.Pool.QueryRow(ctx, fmt.Sprintf(`INSERT INTO %[1]s (prefix, first_number, last_number, ref, reserved_by)
			SELECT $1, coalesce(max(last_number), 0) + 1, coalesce(max(last_number), 0) + $2, $3, $4 FROM %[1]s WHERE prefix = $1
			ON CONFLICT DO NOTHING RETURNING first_number`, n.table()), prefix, count, ref, store.Actor(ctx)).Scan(&first)
		if err == nil {
			if first+count-1 > 99999 {
				return 0, fmt.Errorf("claim numbers: %s has run past 99999", prefix)
			}
			return first, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
	}
	return 0, fmt.Errorf("%w: couldn't reserve claim numbers under %s", store.ErrConflict, prefix)
}
