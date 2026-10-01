package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Move is one container passing the gate.
type Move struct {
	ID              string          `json:"id"`
	Lane            string          `json:"lane"`
	Direction       string          `json:"direction"`
	Status          string          `json:"status"`
	Number          string          `json:"number"`
	TypeCode        string          `json:"type_code"`
	Color           string          `json:"color"`
	Confidence      float64         `json:"confidence"`
	CheckDigitValid bool            `json:"check_digit_valid"`
	Decision        string          `json:"decision"`
	Reason          string          `json:"reason"`
	Result          json.RawMessage `json:"result"`
	Verified        bool            `json:"verified"`
	CorrectedFrom   string          `json:"corrected_from"`
	ResolvedBy      string          `json:"resolved_by"`
	Note            string          `json:"note"`
	IsTest          bool            `json:"is_test"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Photos          []Photo         `json:"photos"`
}

// ReadOutcome is what the engines concluded about a move.
type ReadOutcome struct {
	Status          string
	Number          string
	TypeCode        string
	Color           string
	Confidence      float64
	CheckDigitValid bool
	Decision        string
	Reason          string
	ResultJSON      []byte
}

// ResolveUpdate is a person's decision about a move.
type ResolveUpdate struct {
	Status        string `json:"status"`
	Number        string `json:"number"`
	Verified      bool   `json:"verified"`
	CorrectedFrom string `json:"corrected_from,omitempty"`
	By            string `json:"by"`
	Note          string `json:"note,omitempty"`
}

// ListFilter selects moves, newest first. Before is a move ID (keyset paging).
type ListFilter struct {
	Status string
	Limit  int
	Before string
}

const moveColumns = `id, lane, direction, status, number, type_code, color, confidence, check_digit_valid,
	decision, reason, result_json, verified, corrected_from, resolved_by, note, is_test, created_at, updated_at`

func scanMove(sc scanner) (Move, error) {
	var m Move
	var result, created, updated string
	var cdv, verified, isTest int
	err := sc.Scan(&m.ID, &m.Lane, &m.Direction, &m.Status, &m.Number, &m.TypeCode, &m.Color, &m.Confidence, &cdv,
		&m.Decision, &m.Reason, &result, &verified, &m.CorrectedFrom, &m.ResolvedBy, &m.Note, &isTest, &created, &updated)
	if err != nil {
		return Move{}, err
	}
	if result == "" {
		result = "{}"
	}
	m.Result = json.RawMessage(result)
	m.CheckDigitValid, m.Verified, m.IsTest = cdv == 1, verified == 1, isTest == 1
	m.CreatedAt, m.UpdatedAt = parseTS(created), parseTS(updated)
	m.Photos = []Photo{}
	return m, nil
}

// CreateMove starts a gate-in on a lane.
func (s *Store) CreateMove(ctx context.Context, lane string, isTest bool, actor string) (Move, error) {
	now := s.now()
	m := Move{
		ID: newID(), Lane: lane, Direction: "in", Status: StatusCapturing, Result: json.RawMessage("{}"),
		IsTest: isTest, CreatedAt: now, UpdatedAt: now, Photos: []Photo{},
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO gate_moves (id, tenant_id, depot_id, lane, direction, status, is_test, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, s.tenant, s.depot, lane, m.Direction, m.Status, boolInt(isTest), ts(now), ts(now))
		if err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, m.ID, "move.created", actor, map[string]any{"lane": lane, "is_test": isTest})
	})
	return m, err
}

// GetMove returns a move with its photos.
func (s *Store) GetMove(ctx context.Context, id string) (Move, error) {
	m, err := scanMove(s.db.QueryRowContext(ctx, `SELECT `+moveColumns+` FROM gate_moves WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Move{}, ErrNotFound
	}
	if err != nil {
		return Move{}, err
	}
	m.Photos, err = s.ListPhotos(ctx, id)
	return m, err
}

// ListMoves returns moves newest first, each with its photos.
func (s *Store) ListMoves(ctx context.Context, f ListFilter) ([]Move, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	return s.queryMoves(ctx,
		`SELECT `+moveColumns+` FROM gate_moves
		 WHERE (? = '' OR status = ?) AND (? = '' OR id < ?)
		 ORDER BY id DESC LIMIT ?`,
		f.Status, f.Status, f.Before, f.Before, f.Limit)
}

// VerifiedMoves returns accepted, person-verified, non-practice moves, oldest
// first: the training set and the benchmark set.
func (s *Store) VerifiedMoves(ctx context.Context) ([]Move, error) {
	return s.queryMoves(ctx,
		`SELECT `+moveColumns+` FROM gate_moves
		 WHERE status = 'accepted' AND verified = 1 AND is_test = 0 ORDER BY id`)
}

func (s *Store) queryMoves(ctx context.Context, query string, args ...any) ([]Move, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	moves := []Move{}
	for rows.Next() {
		m, err := scanMove(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		moves = append(moves, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close() // must be closed before the next query (single connection)
	for i := range moves {
		if moves[i].Photos, err = s.ListPhotos(ctx, moves[i].ID); err != nil {
			return nil, err
		}
	}
	return moves, nil
}

// SetStatus changes the status of a move nobody has resolved yet.
func (s *Store) SetStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE gate_moves SET status = ?, updated_at = ? WHERE id = ? AND resolved_by = ''`,
		status, ts(s.now()), id)
	return err
}

// SetResult records what the engines concluded. A move a person has already
// resolved is left alone: the human decision stands.
func (s *Store) SetResult(ctx context.Context, id string, o ReadOutcome) error {
	result := o.ResultJSON
	if len(result) == 0 {
		result = []byte("{}")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE gate_moves SET status = ?, number = ?, type_code = ?, color = ?, confidence = ?,
			 check_digit_valid = ?, decision = ?, reason = ?, result_json = ?, updated_at = ?
			 WHERE id = ? AND resolved_by = ''`,
			o.Status, o.Number, o.TypeCode, o.Color, o.Confidence, boolInt(o.CheckDigitValid),
			o.Decision, o.Reason, string(result), ts(s.now()), id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err
		}
		return s.appendEvent(ctx, tx, id, "move.read", "engine", map[string]any{
			"status": o.Status, "number": o.Number, "decision": o.Decision, "reason": o.Reason,
			"confidence": o.Confidence, "type_code": o.TypeCode, "color": o.Color,
		})
	})
}

// Resolve records a person's decision.
func (s *Store) Resolve(ctx context.Context, id string, u ResolveUpdate) (Move, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE gate_moves SET status = ?, number = ?, verified = ?, corrected_from = ?, resolved_by = ?,
			 note = ?, updated_at = ? WHERE id = ?`,
			u.Status, u.Number, boolInt(u.Verified), u.CorrectedFrom, u.By, u.Note, ts(s.now()), id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return s.appendEvent(ctx, tx, id, "move.resolved", u.By, u)
	})
	if err != nil {
		return Move{}, err
	}
	return s.GetMove(ctx, id)
}
