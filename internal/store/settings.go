package store

import (
	"context"
	"sort"
	"strings"

	sqlcdb "depot/internal/store/db"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
)

// Settings returns the depot's settings whose key starts with prefix. The
// table is tiny, so filtering happens here rather than in SQL.
func (s *Store) Settings(ctx context.Context, prefix string) (map[string]string, error) {
	out := map[string]string{}
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		rows, err := q.ListSettings(ctx, sqlcdb.ListSettingsParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID})
		for _, r := range rows {
			if strings.HasPrefix(r.Key, prefix) {
				out[r.Key] = r.Value
			}
		}
		return err
	})
	return out, err
}

// SetSettings stores values in one transaction. The event names the keys but
// never the values, because settings include passwords. Saving nothing writes
// nothing, not even an event.
func (s *Store) SetSettings(ctx context.Context, values map[string]string, actor string) error {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	now := s.now()
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		for _, k := range keys {
			err := q.UpsertSetting(ctx, sqlcdb.UpsertSettingParams{
				CompanyID: sc.CompanyID, DepotID: sc.DepotID, Key: k, Value: values[k], UpdatedAt: now,
			})
			if err != nil {
				return err
			}
		}
		return s.appendEvent(ctx, q, sc, "settings", "settings.changed", actor, map[string]any{"keys": keys})
	})
}
