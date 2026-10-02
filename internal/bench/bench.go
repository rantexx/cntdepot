// Package bench measures an engine against moves a person has verified.
package bench

import (
	"context"

	"depot/internal/blob"
	"depot/internal/recognition"
	"depot/internal/store"
)

// Miss is a move the engine got wrong.
type Miss struct {
	MoveID string
	Want   string
	Got    string
	Reason string
}

// Report is the benchmark outcome.
type Report struct {
	Moves, MovesCorrect   int
	Photos, PhotosCorrect int
	Errors                int
	InputTokens           int64
	OutputTokens          int64
	TotalLatencyMS        int64
	Misses                []Miss
}

func (r Report) MoveAccuracy() float64  { return ratio(r.MovesCorrect, r.Moves) }
func (r Report) PhotoAccuracy() float64 { return ratio(r.PhotosCorrect, r.Photos) }

// AvgLatencyMS is the mean engine time per photo.
func (r Report) AvgLatencyMS() int64 {
	if r.Photos == 0 {
		return 0
	}
	return r.TotalLatencyMS / int64(r.Photos)
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// Run re-reads up to limit verified moves (0: all) with rec. It reads what
// the live hub reads: deferred camera stills, which nobody chose for reading,
// are skipped, and camera stills are read in ultra-high detail.
func Run(ctx context.Context, st *store.Store, bl *blob.Store, rec recognition.Recognizer, threshold float64, limit int) (Report, error) {
	var rep Report
	moves, err := st.VerifiedMoves(ctx)
	if err != nil {
		return rep, err
	}
	if limit > 0 && len(moves) > limit {
		moves = moves[:limit]
	}
	for _, m := range moves {
		detail := ""
		if m.Source == store.SourceCamera {
			detail = recognition.DetailUltraHigh
		}
		var frames []recognition.Frame
		for _, p := range m.Photos {
			if p.Deferred {
				continue
			}
			data, err := bl.Get(p.AISHA256)
			if err != nil {
				return rep, err
			}
			att, err := rec.Read(ctx, recognition.Image{Data: data, MediaType: p.AIMediaType, Detail: detail})
			rep.Photos++
			rep.InputTokens += att.Usage.InputTokens
			rep.OutputTokens += att.Usage.OutputTokens
			rep.TotalLatencyMS += att.LatencyMS
			if err != nil {
				rep.Errors++
				frames = append(frames, recognition.ErrorFrame(p.Role, err.Error()))
				continue
			}
			f := recognition.BuildFrame(p.Role, att.Reading)
			if len(f.Candidates) > 0 && f.Candidates[0].Number == m.Number {
				rep.PhotosCorrect++
			}
			frames = append(frames, f)
		}
		res := recognition.Reconcile(frames, threshold)
		rep.Moves++
		if res.Number == m.Number {
			rep.MovesCorrect++
		} else {
			rep.Misses = append(rep.Misses, Miss{MoveID: m.ID, Want: m.Number, Got: res.Number, Reason: res.Reason})
		}
	}
	return rep, nil
}
