package store

import (
	"context"
	"encoding/json"
	"time"

	sqlcdb "depot/internal/store/db"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
)

// Event is one entry of the append-only log.
type Event struct {
	Seq        int64           `json:"seq"`
	ID         string          `json:"id"`
	Stream     string          `json:"stream"`
	Kind       string          `json:"kind"`
	Actor      string          `json:"actor"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurred_at"`
}

// Events returns a stream's events in order.
func (s *Store) Events(ctx context.Context, stream string) ([]Event, error) {
	events := []Event{}
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		rows, err := q.ListEvents(ctx, sqlcdb.ListEventsParams{CompanyID: sc.CompanyID, Stream: stream})
		for _, r := range rows {
			events = append(events, Event{
				Seq: r.Seq, ID: r.ID, Stream: r.Stream, Kind: r.Kind, Actor: r.Actor,
				Payload: r.Payload, OccurredAt: r.OccurredAt,
			})
		}
		return err
	})
	return events, err
}
