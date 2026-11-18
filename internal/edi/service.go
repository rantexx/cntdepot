package edi

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"depot/internal/blob"
	edidb "depot/internal/edi/db"
	"depot/internal/edifact"
	"depot/internal/mail"
	"depot/internal/tenancy"
	"depot/internal/yard"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotProcessable: only an unmatched or error inbound message is processed again.
var ErrNotProcessable = errors.New("only an unmatched or error message can be processed again")

// Config: From is the address mail is sent from, InboundDomain the domain of
// the companies' edi-<token>@ addresses, and Tick the worker period.
type Config struct {
	From, InboundDomain string
	Tick                time.Duration
}

// Service handles the EDI files in and out of every company.
type Service struct {
	st   *Store
	blob *blob.Store
	yard *yard.Service
	mail mail.Mailer
	cfg  Config
}

func NewService(pool *pgxpool.Pool, bl *blob.Store, y *yard.Service, m mail.Mailer, cfg Config) *Service {
	return &Service{st: NewStore(pool), blob: bl, yard: y, mail: m, cfg: cfg}
}

// Message is the API view of an edi_messages row; "" stands for a NULL link.
type Message struct {
	ID, Direction, Kind, Status                string
	LineID, DepotID, MoveID, OrderID, AnswerOf string
	Function, ControlRef, MessageRef           string
	BlobDigest, FileName, Sender               string
	Problems                                   []edifact.Problem
	Tries                                      int64
	CreatedAt                                  time.Time
	SentAt                                     *time.Time
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func messageOf(r edidb.EdiMessage) (Message, error) {
	m := Message{
		ID: r.ID, Direction: r.Direction, Kind: r.Kind, Status: r.Status,
		LineID: str(r.LineID), DepotID: str(r.DepotID), MoveID: str(r.MoveID), OrderID: str(r.OrderID), AnswerOf: str(r.AnswerOf),
		Function: r.Function, ControlRef: r.ControlRef, MessageRef: r.MessageRef,
		BlobDigest: r.BlobDigest, FileName: r.FileName, Sender: r.Sender,
		Tries: r.Tries, CreatedAt: r.CreatedAt, SentAt: r.SentAt,
	}
	return m, json.Unmarshal(r.Problems, &m.Problems)
}

// Message returns one message of the company.
func (s *Service) Message(ctx context.Context, id string) (m Message, err error) {
	err = s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		r, err := q.GetMessage(ctx, edidb.GetMessageParams{CompanyID: sc.CompanyID, ID: id})
		if err != nil {
			return notFound(err)
		}
		m, err = messageOf(r)
		return err
	})
	return m, err
}

// problemsJSON never stores null.
func problemsJSON(ps []edifact.Problem) json.RawMessage {
	if len(ps) == 0 {
		return json.RawMessage("[]")
	}
	b, _ := json.Marshal(ps) // strings only: cannot fail
	return b
}
