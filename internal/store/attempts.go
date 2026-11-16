package store

import (
	"context"
	"encoding/json"
	"time"

	sqlcdb "depot/internal/store/db"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
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
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		if err := exists(ctx, q, sc, a.MoveID); err != nil {
			return err
		}
		return q.InsertAttempt(ctx, sqlcdb.InsertAttemptParams{
			CompanyID: sc.CompanyID, ID: a.ID, MoveID: a.MoveID, PhotoID: a.PhotoID, Engine: a.Engine, Model: a.Model,
			PromptVersion: a.PromptVersion, ReadingJson: a.Reading, Raw: a.Raw, Error: a.Error,
			InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, LatencyMs: a.LatencyMS, CreatedAt: a.CreatedAt,
		})
	})
	return a, err
}

// ListAttempts returns a move's engine runs, oldest first.
func (s *Store) ListAttempts(ctx context.Context, moveID string) ([]Attempt, error) {
	attempts := []Attempt{}
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		rows, err := q.ListAttempts(ctx, sqlcdb.ListAttemptsParams{CompanyID: sc.CompanyID, MoveID: moveID})
		for _, r := range rows {
			attempts = append(attempts, Attempt{
				ID: r.ID, MoveID: r.MoveID, PhotoID: r.PhotoID, Engine: r.Engine, Model: r.Model,
				PromptVersion: r.PromptVersion, Reading: r.ReadingJson, Raw: r.Raw, Error: r.Error,
				InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, LatencyMS: r.LatencyMs, CreatedAt: r.CreatedAt,
			})
		}
		return err
	})
	return attempts, err
}
