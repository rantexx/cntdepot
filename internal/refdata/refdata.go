// Package refdata loads the reference lists (SMDG liner codes, UN/LOCODE)
// into the ref_* tables. No list is bundled: both licences need written
// permission first (see README.md). Dropping data/smdg_liners.csv or
// data/unlocode.csv in and setting the version constant loads it on migrate.
package refdata

import (
	"context"
	"embed"
	"encoding/csv"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
)

// "none" means no file is bundled and the table stays empty.
const (
	SMDGVersion   = "none"
	LocodeVersion = "none"
)

// all: keeps data/.gitkeep so the pattern matches while no CSV is bundled.
//
//go:embed all:data
var files embed.FS

// Load replaces each ref table when ref_versions differs from the embedded
// version, and does nothing when they are equal. Run it as the table owner.
func Load(ctx context.Context, tx pgx.Tx) error {
	data, err := fs.Sub(files, "data")
	if err != nil {
		return err
	}
	return load(ctx, tx, data, SMDGVersion, LocodeVersion)
}

func load(ctx context.Context, tx pgx.Tx, fsys fs.FS, smdgVersion, locodeVersion string) error {
	err := replace(ctx, tx, fsys, "smdg_liners.csv", "ref_smdg_liners", []string{"code", "name", "version"}, 2, smdgVersion,
		func(r []string) []any { return []any{r[0], r[1]} })
	if err != nil {
		return err
	}
	return replace(ctx, tx, fsys, "unlocode.csv", "ref_locodes", []string{"code", "country", "name", "function", "version"}, 4, locodeVersion,
		func(r []string) []any { return []any{r[0] + r[1], r[0], r[2], r[3]} })
}

// replace swaps table's rows for the CSV's (header skipped, ncols columns)
// unless ref_versions already holds version. A missing file means no rows.
func replace(ctx context.Context, tx pgx.Tx, fsys fs.FS, file, table string, cols []string, ncols int, version string, row func([]string) []any) error {
	var have string
	err := tx.QueryRow(ctx, "SELECT version FROM ref_versions WHERE name = $1", table).Scan(&have)
	if err == nil && have == version {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var rows [][]any
	f, err := fsys.Open(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		defer f.Close()
		r := csv.NewReader(f)
		r.FieldsPerRecord = ncols
		recs, err := r.ReadAll()
		if err != nil {
			return fmt.Errorf("refdata: %s: %w", file, err)
		}
		for _, rec := range recs[min(1, len(recs)):] {
			rows = append(rows, append(row(rec), version))
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM "+table); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("refdata: %s: %w", table, err)
	}
	_, err = tx.Exec(ctx, "INSERT INTO ref_versions (name, version) VALUES ($1, $2) ON CONFLICT (name) DO UPDATE SET version = $2", table, version)
	return err
}
