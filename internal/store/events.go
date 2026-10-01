package store

import (
	"context"
	"encoding/json"
	"time"
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
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, id, stream, kind, actor, payload, occurred_at FROM events WHERE stream = ? ORDER BY seq`, stream)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var e Event
		var payload, occurred string
		if err := rows.Scan(&e.Seq, &e.ID, &e.Stream, &e.Kind, &e.Actor, &payload, &occurred); err != nil {
			return nil, err
		}
		e.Payload, e.OccurredAt = json.RawMessage(payload), parseTS(occurred)
		events = append(events, e)
	}
	return events, rows.Err()
}
