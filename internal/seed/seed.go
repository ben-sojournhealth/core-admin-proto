// Package seed loads protojson fixtures into Repos. Each subdirectory of
// the seed file system holds one resource type, one resource per .json
// file.
// Parsing is strict (unknown fields are errors) so fixtures stay in sync
// with the protos.
package seed

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"

	"google.golang.org/protobuf/encoding/protojson"

	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
)

// Load reads every known subdirectory of fsys into r. Missing
// subdirectories are skipped. Returns the number of resources loaded.
func Load(ctx context.Context, r *repo.Repos, fsys fs.FS) (int, error) {
	loaders := []struct {
		subdir string
		load   func(context.Context, fs.FS, string) (int, error)
	}{
		{"customers", loader(r.Customers)},
		{"groups", loader(r.Groups)},
		{"plans", loader(r.Plans)},
		{"underlying_networks", loader(r.UnderlyingNetworks)},
		{"networks", loader(r.Networks)},
		{"fee_schedules", loader(r.FeeSchedules)},
		{"contracts", loader(r.Contracts)},
		{"providers", loader(r.Providers)},
		{"participations", loader(r.Participations)},
		{"members", loader(r.Members)},
		{"claims", loader(r.Claims)},
		{"stoploss_policies", loader(r.StopLossPolicies)},
	}
	total := 0
	for _, l := range loaders {
		n, err := l.load(ctx, fsys, l.subdir)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func loader[T store.Resource](s store.Store[T]) func(context.Context, fs.FS, string) (int, error) {
	return func(ctx context.Context, fsys fs.FS, dir string) (int, error) {
		files, err := fs.Glob(fsys, path.Join(dir, "*.json"))
		if err != nil {
			return 0, err
		}
		sort.Strings(files)
		for _, f := range files {
			b, err := fs.ReadFile(fsys, f)
			if err != nil {
				return 0, err
			}
			var zero T
			msg := zero.ProtoReflect().Type().New().Interface().(T)
			if err := protojson.Unmarshal(b, msg); err != nil {
				return 0, fmt.Errorf("seed: %s: %w", f, err)
			}
			if _, err := s.Create(store.WithActor(ctx, "seed"), msg); err != nil {
				return 0, fmt.Errorf("seed: %s: %w", f, err)
			}
		}
		return len(files), nil
	}
}
