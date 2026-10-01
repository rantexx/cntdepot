package store

import (
	"context"
	"encoding/json"
	"time"
)

// Attempt is one engine run on one photo. The table is append-only: raw
// engine output is kept forever beside the resolved value.
type Attempt struct {
	ID            string          `json:"id"`
	MoveID        string          `json:"move_id"`
	PhotoID       string          `json:"photo_id"`
	Engine        string          `json:"engine"`
	Model         string          `json:"model"`
	PromptVersion string          `json:"prompt_version"`
	Reading       json.RawMessage `json:"reading"`
	Raw           string          `json:"raw"`
	Error         string          `json:"error"`
	InputTokens   int64           `json:"input_tokens"`
	OutputTokens  int64           `json:"output_tokens"`
	LatencyMS     int64           `json:"latency_ms"`
	CreatedAt     time.Time       `json:"created_at"`
}

// AddAttempt records an engine run.
func (s *Store) AddAttempt(ctx context.Context, a Attempt) (Attempt, error) {
	a.ID, a.CreatedAt = newID(), s.now()
	if len(a.Reading) == 0 {
		a.Reading = json.RawMessage("{}")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO recognition_attempts (id, tenant_id, move_id, photo_id, engine, model, prompt_version,
		 reading_json, raw, error, input_tokens, output_tokens, latency_ms, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, s.tenant, a.MoveID, a.PhotoID, a.Engine, a.Model, a.PromptVersion,
		string(a.Reading), a.Raw, a.Error, a.InputTokens, a.OutputTokens, a.LatencyMS, ts(a.CreatedAt))
	return a, err
}

// ListAttempts returns a move's engine runs, oldest first.
func (s *Store) ListAttempts(ctx context.Context, moveID string) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, move_id, photo_id, engine, model, prompt_version, reading_json, raw, error,
		 input_tokens, output_tokens, latency_ms, created_at
		 FROM recognition_attempts WHERE move_id = ? ORDER BY id`, moveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attempts := []Attempt{}
	for rows.Next() {
		var a Attempt
		var reading, created string
		if err := rows.Scan(&a.ID, &a.MoveID, &a.PhotoID, &a.Engine, &a.Model, &a.PromptVersion, &reading, &a.Raw,
			&a.Error, &a.InputTokens, &a.OutputTokens, &a.LatencyMS, &created); err != nil {
			return nil, err
		}
		a.Reading, a.CreatedAt = json.RawMessage(reading), parseTS(created)
		attempts = append(attempts, a)
	}
	return attempts, rows.Err()
}
