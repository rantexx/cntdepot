package store

import (
	"context"
	"encoding/json"
	"time"

	sqlcdb "depot/internal/store/db"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
)

// Move is one container passing the gate.
type Move struct {
	ID              string          `json:"id"`
	Lane            string          `json:"lane"`
	Direction       string          `json:"direction"`
	Source          string          `json:"source"` // phone | camera
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
	Actor           string // who concluded it; empty: "engine"
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
	Source string
	Limit  int
	Before string
}

func moveFrom(r sqlcdb.GateMove) Move {
	res := r.ResultJson
	if len(res) == 0 {
		res = json.RawMessage("{}")
	}
	return Move{
		ID: r.ID, Lane: r.Lane, Direction: r.Direction, Source: r.Source, Status: r.Status, Number: r.Number,
		TypeCode: r.TypeCode, Color: r.Color, Confidence: r.Confidence, CheckDigitValid: r.CheckDigitValid,
		Decision: r.Decision, Reason: r.Reason, Result: res, Verified: r.Verified, CorrectedFrom: r.CorrectedFrom,
		ResolvedBy: r.ResolvedBy, Note: r.Note, IsTest: r.IsTest, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Photos: []Photo{},
	}
}

// withPhotos builds moves from rows and fills in their photos with one query.
func withPhotos(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, rows []sqlcdb.GateMove) ([]Move, error) {
	moves := make([]Move, len(rows))
	ids := make([]string, len(rows))
	at := map[string]int{}
	for i, r := range rows {
		moves[i], ids[i], at[r.ID] = moveFrom(r), r.ID, i
	}
	if len(ids) == 0 {
		return moves, nil
	}
	photos, err := q.ListPhotosOfMoves(ctx, sqlcdb.ListPhotosOfMovesParams{CompanyID: sc.CompanyID, MoveIds: ids})
	if err != nil {
		return nil, err
	}
	for _, p := range photos {
		m := &moves[at[p.Photo.MoveID]]
		m.Photos = append(m.Photos, photoFrom(p.Photo, p.DepotID))
	}
	return moves, nil
}

// CreateMove starts a phone gate-in on a lane.
func (s *Store) CreateMove(ctx context.Context, lane string, isTest bool, actor string) (Move, error) {
	return s.CreateMoveWithSource(ctx, lane, SourcePhone, isTest, actor)
}

// CreateMoveWithSource starts a gate-in whose photos come from source.
func (s *Store) CreateMoveWithSource(ctx context.Context, lane, source string, isTest bool, actor string) (Move, error) {
	now := s.now()
	m := Move{
		ID: newID(), Lane: lane, Direction: "in", Source: source, Status: StatusCapturing, Result: json.RawMessage("{}"),
		IsTest: isTest, CreatedAt: now, UpdatedAt: now, Photos: []Photo{},
	}
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		if sc.DepotID == "" {
			return tenancy.ErrNoScope // a move belongs to one depot
		}
		err := q.InsertMove(ctx, sqlcdb.InsertMoveParams{
			CompanyID: sc.CompanyID, ID: m.ID, DepotID: sc.DepotID, Lane: lane, Direction: m.Direction,
			Source: source, Status: m.Status, IsTest: isTest, CreatedAt: now,
		})
		if err != nil {
			return err
		}
		return s.appendEvent(ctx, q, sc, m.ID, "move.created", actor, map[string]any{"lane": lane, "source": source, "is_test": isTest})
	})
	return m, err
}

// GetMove returns a move with its photos.
func (s *Store) GetMove(ctx context.Context, id string) (Move, error) {
	var m Move
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) (err error) {
		m, err = getMove(ctx, q, sc, id)
		return err
	})
	return m, err
}

func getMove(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, id string) (Move, error) {
	r, err := q.GetMove(ctx, sqlcdb.GetMoveParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id})
	if err != nil {
		return Move{}, notFound(err)
	}
	ms, err := withPhotos(ctx, q, sc, []sqlcdb.GateMove{r})
	if err != nil {
		return Move{}, err
	}
	return ms[0], nil
}

// ListMoves returns moves newest first, each with its photos.
func (s *Store) ListMoves(ctx context.Context, f ListFilter) ([]Move, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	var moves []Move
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		rows, err := q.ListMoves(ctx, sqlcdb.ListMovesParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, Status: f.Status, Source: f.Source,
			Before: f.Before, RowLimit: int32(f.Limit),
		})
		if err != nil {
			return err
		}
		moves, err = withPhotos(ctx, q, sc, rows)
		return err
	})
	return moves, err
}

// VerifiedMoves returns accepted, person-verified, non-practice moves, oldest
// first: the training set and the benchmark set.
func (s *Store) VerifiedMoves(ctx context.Context) ([]Move, error) {
	var moves []Move
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		rows, err := q.VerifiedMoves(ctx, sqlcdb.VerifiedMovesParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID})
		if err != nil {
			return err
		}
		moves, err = withPhotos(ctx, q, sc, rows)
		return err
	})
	return moves, err
}

// exists returns the move, or ErrNotFound when it is not in the scope's company and depot.
func exists(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, id string) (sqlcdb.GateMove, error) {
	m, err := q.GetMove(ctx, sqlcdb.GetMoveParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id})
	return m, notFound(err)
}

// SetStatus changes the status of a move nobody has resolved yet. A move that
// is already resolved is left alone; an unknown id is ErrNotFound.
func (s *Store) SetStatus(ctx context.Context, id, status string) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		err := q.SetStatus(ctx, sqlcdb.SetStatusParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, Status: status, UpdatedAt: s.now(),
		})
		if err != nil {
			return err
		}
		_, err = exists(ctx, q, sc, id)
		return err
	})
}

// SetResult records what the engines concluded. A move a person has already
// resolved is left alone: the human decision stands. An unknown id is ErrNotFound.
func (s *Store) SetResult(ctx context.Context, id string, o ReadOutcome) error {
	result := o.ResultJSON
	if len(result) == 0 {
		result = []byte("{}")
	}
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		n, err := q.SetResult(ctx, sqlcdb.SetResultParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, Status: o.Status, Number: o.Number,
			TypeCode: o.TypeCode, Color: o.Color, Confidence: o.Confidence, CheckDigitValid: o.CheckDigitValid,
			Decision: o.Decision, Reason: o.Reason, ResultJson: result, UpdatedAt: s.now(),
		})
		if err != nil {
			return err
		}
		if n == 0 {
			_, err = exists(ctx, q, sc, id)
			return err
		}
		actor := o.Actor
		if actor == "" {
			actor = "engine"
		}
		return s.appendEvent(ctx, q, sc, id, "move.read", actor, map[string]any{
			"status": o.Status, "number": o.Number, "decision": o.Decision, "reason": o.Reason,
			"confidence": o.Confidence, "type_code": o.TypeCode, "color": o.Color,
		})
	})
}

// Resolve records a person's decision.
func (s *Store) Resolve(ctx context.Context, id string, u ResolveUpdate) (Move, error) {
	var m Move
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		n, err := q.ResolveMove(ctx, sqlcdb.ResolveMoveParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, Status: u.Status, Number: u.Number,
			Verified: u.Verified, CorrectedFrom: u.CorrectedFrom, ResolvedBy: u.By, Note: u.Note, UpdatedAt: s.now(),
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if err := s.appendEvent(ctx, q, sc, id, "move.resolved", u.By, u); err != nil {
			return err
		}
		m, err = getMove(ctx, q, sc, id)
		return err
	})
	return m, err
}
