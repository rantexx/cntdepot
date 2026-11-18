package edi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	edidb "depot/internal/edi/db"
	"depot/internal/edifact"
	"depot/internal/mail"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
)

// Run calls Tick every cfg.Tick (default 30 s) until ctx ends. It logs
// errors and never panics. Nothing on the gate's path calls it.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(cmp.Or(s.cfg.Tick, 30*time.Second))
	defer t.Stop()
	for {
		s.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) pass(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("edi: worker panic: %v", r)
		}
	}()
	if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
		log.Printf("edi: %v", err)
	}
}

// Tick is one worker pass over every company with EDI, each in its own
// scope: inbound files left "received", then CODECO from gate events, then
// sending what is due. One company's failure does not stop the others.
func (s *Service) Tick(ctx context.Context) error {
	cs, err := s.st.Companies(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range cs {
		if err := s.company(tenancy.With(ctx, tenancy.Scope{CompanyID: c, UserID: mailActor})); err != nil {
			errs = append(errs, fmt.Errorf("company %s: %w", c, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) company(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	sc, _ := tenancy.From(ctx)
	return errors.Join(s.retryStuck(ctx), s.queueGateEvents(ctx), s.sendDue(ctx), s.reports(ctx, sc))
}

// retryStuck processes inbound files left "received" for over a minute,
// once: a file that fails again becomes an error for the office to process.
func (s *Service) retryStuck(ctx context.Context) error {
	var ids []string
	if err := s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) (err error) {
		ids, err = q.StuckInbound(ctx, edidb.StuckInboundParams{CompanyID: sc.CompanyID, Before: s.now().Add(-time.Minute)})
		return err
	}); err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		_, perr := s.processStored(ctx, id)
		if perr == nil {
			continue
		}
		problems := problemsJSON([]edifact.Problem{{Code: "E-PROC", Text: "the file could not be processed: " + perr.Error()}})
		if err := s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
			return q.FailStuck(ctx, edidb.FailStuckParams{CompanyID: sc.CompanyID, ID: id, Problems: problems})
		}); err != nil {
			errs = append(errs, fmt.Errorf("message %s left received: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// sendDue sends the company's due messages one by one, each claimed in its
// own transaction until it is sent or rescheduled.
func (s *Service) sendDue(ctx context.Context) error {
	ls, err := s.st.lines(ctx)
	if err != nil {
		return err
	}
	lines := map[string]LineEDI{}
	for _, l := range ls {
		lines[l.LineID] = l
	}
	for {
		done := false
		err := s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
			r, err := q.ClaimDue(ctx, edidb.ClaimDueParams{CompanyID: sc.CompanyID, Now: ptr(s.now())})
			if errors.Is(err, pgx.ErrNoRows) {
				done = true
				return nil
			}
			if err != nil {
				return err
			}
			return s.sendOne(ctx, q, sc.CompanyID, r, lines[str(r.LineID)].Profile)
		})
		if err != nil || done {
			return err
		}
	}
}

// retryAfter is the wait after the n-th failed try: 1, 5 and 30 minutes, then hourly.
func retryAfter(n int64) time.Duration {
	switch n {
	case 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 30 * time.Minute
	}
	return time.Hour
}

// sendOne mails one claimed message to the line. A failed try is retried
// later; a day after the first try, with no address or with its file lost,
// the message fails.
func (s *Service) sendOne(ctx context.Context, q *edidb.Queries, company string, r edidb.ClaimDueRow, p Profile) error {
	now := s.now()
	first := now
	if r.FirstTryAt != nil {
		first = *r.FirstTryAt
	}
	tried := edidb.SetTriedParams{CompanyID: company, ID: r.ID, Status: "pending", FirstTryAt: &first, NextAttemptAt: ptr(now.Add(retryAfter(0)))}
	if len(p.SendTo) == 0 {
		tried.Status, tried.Problems = "failed", problemsJSON([]edifact.Problem{{Code: "E-NOADDR", Text: "the line has no address to send to"}})
		return q.SetTried(ctx, tried)
	}
	data, err := s.blob.Get(r.BlobDigest)
	if err != nil { // a lost file fails its row only: the queue moves on
		tried.Status, tried.Problems = "failed", problemsJSON([]edifact.Problem{{Code: "E-SEND", Text: "file missing: " + err.Error()}})
		return q.SetTried(ctx, tried)
	}
	subject := strings.Join(strings.Fields(strings.ToUpper(r.Kind)+" "+r.DepotName+" "+r.Ref), " ")
	err = s.mail.Send(ctx, mail.Outgoing{To: p.SendTo, Subject: subject, Text: subject,
		Attachments: []mail.Attachment{{Name: r.FileName, ContentType: "application/EDIFACT", Data: data}}})
	if err == nil {
		return q.SetSent(ctx, edidb.SetSentParams{CompanyID: company, ID: r.ID, SentAt: &now})
	}
	tried.NextAttemptAt = ptr(now.Add(retryAfter(r.Tries + 1)))
	tried.Problems = problemsJSON([]edifact.Problem{{Code: "E-SEND", Text: err.Error()}})
	if now.Sub(first) > 24*time.Hour {
		tried.Status = "failed"
	}
	return q.SetTried(ctx, tried)
}
