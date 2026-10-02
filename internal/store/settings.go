package store

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

// Settings returns every setting whose key starts with prefix. The table is
// tiny, so filtering happens here rather than in SQL.
func (s *Store) Settings(ctx context.Context, prefix string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if strings.HasPrefix(k, prefix) {
			out[k] = v
		}
	}
	return out, rows.Err()
}

// SetSettings stores values in one transaction. The event names the keys but
// never the values, because settings include passwords.
func (s *Store) SetSettings(ctx context.Context, values map[string]string, actor string) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	now := ts(s.now())
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, k := range keys {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
				 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
				k, values[k], now); err != nil {
				return err
			}
		}
		return s.appendEvent(ctx, tx, "settings", "settings.changed", actor, map[string]any{"keys": keys})
	})
}
