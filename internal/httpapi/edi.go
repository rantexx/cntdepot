package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"log"
	"math"
	"mime"
	"net/http"

	"depot/internal/api"
	"depot/internal/edi"
	"depot/internal/mail"
)

// Shipping-line EDI: messages, line settings, status, and the Postmark inbound webhook.

const (
	maxEDIFile     = 2 << 20  // one uploaded EDI file
	maxInbound     = 40 << 20 // one Postmark inbound webhook body
	maxEDIPage     = 500
	defaultEDIPage = 50
)

// ediError is the answer to an EDI error, or nil.
func ediError(err error) *apiError {
	var inv edi.Invalid
	switch {
	case errors.As(err, &inv):
		return &apiError{http.StatusBadRequest, "E4000", string(inv)}
	case errors.Is(err, edi.ErrNotFound):
		return &apiError{http.StatusNotFound, "E4043", "no such record"}
	case errors.Is(err, edi.ErrNotProcessable), errors.Is(err, edi.ErrNotResendable), errors.Is(err, edi.ErrNotOnEDI):
		return &apiError{http.StatusConflict, "E4098", err.Error()}
	}
	return nil
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// apiMessage is m for the API; via and trusted mean something only for inbound files.
func apiMessage(m edi.Message) api.EDIMessage {
	out := api.EDIMessage{
		Id: m.ID, Direction: api.EDIDirection(m.Direction), Kind: api.EDIKind(m.Kind), Status: api.EDIStatus(m.Status),
		LineId: optStr(m.LineID), DepotId: optStr(m.DepotID), MoveId: optStr(m.MoveID), OrderId: optStr(m.OrderID), AnswerOf: optStr(m.AnswerOf),
		Function: m.Function, ControlRef: m.ControlRef, MessageRef: m.MessageRef, FileName: m.FileName, Sender: m.Sender,
		Problems: make([]api.EDIProblem, len(m.Problems)), Tries: int(m.Tries), CreatedAt: m.CreatedAt, SentAt: m.SentAt,
	}
	for i, p := range m.Problems {
		out.Problems[i] = api.EDIProblem{Code: p.Code, Text: p.Text}
	}
	if m.Direction == "in" {
		via, trusted := api.EDIMessageVia(m.Via), m.Trusted
		out.Via, out.Trusted = &via, &trusted
	}
	return out
}

func apiMessages(ms []edi.Message) []api.EDIMessage {
	out := make([]api.EDIMessage, len(ms))
	for i, m := range ms {
		out[i] = apiMessage(m)
	}
	return out
}

func apiLine(l edi.LineEDI) api.LineEDI {
	p := l.Profile
	tm := make([]api.TypePair, len(p.TypeMap))
	for i, m := range p.TypeMap {
		tm[i] = api.TypePair{Ours: m[0], Theirs: m[1]}
	}
	return api.LineEDI{LineId: l.LineID, Code: l.Code, Mode: api.LineEDIMode(l.Mode), Profile: api.LineEDIProfile{
		SenderId: p.SenderID, SenderQual: p.SenderQual, RecipientId: p.RecipientID, RecipientQual: p.RecipientQual, LineCode: p.LineCode,
		SendTo: list(p.SendTo), AllowedFrom: list(p.AllowedFrom), EmptiesOnly: p.EmptiesOnly, SendAperak: p.SendAPERAK, Test: p.Test,
		TypeMap: tm, ReportTo: list(p.ReportTo), ReportTime: p.ReportTime,
	}}
}

func (s *Server) ListEdiMessages(ctx context.Context, req api.ListEdiMessagesRequestObject) (api.ListEdiMessagesResponseObject, error) {
	p := req.Params
	limit := val(p.Limit)
	if limit < 1 {
		limit = defaultEDIPage
	}
	ms, total, err := s.opts.EDI.ListMessages(ctx, edi.Filter{
		Direction: string(val(p.Direction)), Kind: string(val(p.Kind)), Status: string(val(p.Status)), LineID: val(p.LineId), From: p.From, To: p.To,
		Depots: allowedDepots(ctx),
	}, min(limit, maxEDIPage), min(max(val(p.Offset), 0), math.MaxInt32)) // the query takes 32-bit numbers
	if err != nil {
		return nil, err
	}
	return api.ListEdiMessages200JSONResponse{Messages: apiMessages(ms), Total: total}, nil
}

// visible returns a message the member may see: one of no depot or of a
// depot of their membership. Any other is not found, as in another company.
func (s *Server) visible(ctx context.Context, id string) (edi.Message, error) {
	m, err := s.opts.EDI.Message(ctx, id)
	if err == nil && m.DepotID != "" && !depotAllowed(ctx, m.DepotID) {
		return edi.Message{}, edi.ErrNotFound
	}
	return m, err
}

func (s *Server) GetEdiMessage(ctx context.Context, req api.GetEdiMessageRequestObject) (api.GetEdiMessageResponseObject, error) {
	m, err := s.visible(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return api.GetEdiMessage200JSONResponse(apiMessage(m)), nil
}

func (s *Server) GetEdiMessageRaw(ctx context.Context, req api.GetEdiMessageRawRequestObject) (api.GetEdiMessageRawResponseObject, error) {
	if _, err := s.visible(ctx, req.Id); err != nil {
		return nil, err
	}
	m, data, err := s.opts.EDI.Raw(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	// The file name came with the file (a mail attachment): FormatMediaType quotes or refuses it.
	disp := mime.FormatMediaType("attachment", map[string]string{"filename": m.FileName})
	if m.FileName == "" || disp == "" {
		disp = mime.FormatMediaType("attachment", map[string]string{"filename": m.ID + ".edi"})
	}
	return api.GetEdiMessageRaw200ApplicationEDIFACTResponse{Body: bytes.NewReader(data), ContentLength: int64(len(data)),
		Headers: api.GetEdiMessageRaw200ResponseHeaders{ContentDisposition: &disp}}, nil
}

func (s *Server) ResendEdiMessage(ctx context.Context, req api.ResendEdiMessageRequestObject) (api.ResendEdiMessageResponseObject, error) {
	if _, err := s.visible(ctx, req.Id); err != nil {
		return nil, err
	}
	return api.ResendEdiMessage204Response{}, s.opts.EDI.Resend(ctx, req.Id, actor(ctx))
}

func (s *Server) ProcessEdiMessage(ctx context.Context, req api.ProcessEdiMessageRequestObject) (api.ProcessEdiMessageResponseObject, error) {
	var lineID, depotID string
	if b := req.Body; b != nil {
		lineID, depotID = val(b.LineId), val(b.DepotId)
	}
	if _, err := s.visible(ctx, req.Id); err != nil {
		return nil, err
	}
	if depotID != "" && !depotAllowed(ctx, depotID) {
		return nil, edi.ErrNotFound
	}
	m, err := s.opts.EDI.Process(ctx, req.Id, lineID, depotID, actor(ctx))
	if err != nil {
		return nil, err
	}
	return api.ProcessEdiMessage200JSONResponse(apiMessage(m)), nil
}

// UploadEdi stores and processes each uploaded file; none is stored when one is over 2 MiB.
func (s *Server) UploadEdi(ctx context.Context, req api.UploadEdiRequestObject) (api.UploadEdiResponseObject, error) {
	form, err := readForm(req.Body)
	if err != nil {
		return nil, err
	}
	defer form.RemoveAll()
	files := form.File["file"]
	if len(files) == 0 {
		return nil, fail(http.StatusBadRequest, "E4003", "invalid upload: the file part is missing")
	}
	for _, f := range files {
		if f.Size > maxEDIFile {
			return nil, fail(http.StatusRequestEntityTooLarge, "E4130", f.Filename+" is over 2 MiB")
		}
	}
	var out []edi.Message
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, err
		}
		ms, err := s.opts.EDI.Upload(ctx, fh.Filename, data, actor(ctx))
		if err != nil {
			return nil, err
		}
		out = append(out, ms...)
	}
	return api.UploadEdi200JSONResponse{Messages: apiMessages(out)}, nil
}

func (s *Server) ListLineEdi(ctx context.Context, _ api.ListLineEdiRequestObject) (api.ListLineEdiResponseObject, error) {
	ls, err := s.opts.EDI.Lines(ctx)
	if err != nil {
		return nil, err
	}
	out := make(api.ListLineEdi200JSONResponse, len(ls))
	for i, l := range ls {
		out[i] = apiLine(l)
	}
	return out, nil
}

func (s *Server) SetLineEdi(ctx context.Context, req api.SetLineEdiRequestObject) (api.SetLineEdiResponseObject, error) {
	b := req.Body
	tm := make([][2]string, len(b.Profile.TypeMap))
	for i, m := range b.Profile.TypeMap {
		tm[i] = [2]string{m.Ours, m.Theirs}
	}
	p := b.Profile
	l, err := s.opts.EDI.SetLine(ctx, edi.LineEDI{LineID: req.LineId, Mode: string(b.Mode), Profile: edi.Profile{
		SenderID: p.SenderId, SenderQual: p.SenderQual, RecipientID: p.RecipientId, RecipientQual: p.RecipientQual, LineCode: p.LineCode,
		SendTo: p.SendTo, AllowedFrom: p.AllowedFrom, EmptiesOnly: p.EmptiesOnly, SendAPERAK: p.SendAperak, Test: p.Test,
		TypeMap: tm, ReportTo: p.ReportTo, ReportTime: p.ReportTime,
	}})
	if err != nil {
		return nil, err
	}
	return api.SetLineEdi200JSONResponse(apiLine(l)), nil
}

func (s *Server) TestLineEdi(ctx context.Context, req api.TestLineEdiRequestObject) (api.TestLineEdiResponseObject, error) {
	m, err := s.opts.EDI.SendTest(ctx, req.LineId, actor(ctx))
	if err != nil {
		return nil, err
	}
	return api.TestLineEdi200JSONResponse(apiMessage(m)), nil
}

func (s *Server) GetEdiStatus(ctx context.Context, _ api.GetEdiStatusRequestObject) (api.GetEdiStatusResponseObject, error) {
	st, err := s.opts.EDI.Status(ctx)
	if err != nil {
		return nil, err
	}
	return api.GetEdiStatus200JSONResponse{Mail: api.EDIStatusInfoMail(st.Mail), InboundAddress: st.InboundAddress,
		PartyId: st.PartyID, Problems: st.Problems}, nil
}

func (s *Server) SetEdiCompany(ctx context.Context, req api.SetEdiCompanyRequestObject) (api.SetEdiCompanyResponseObject, error) {
	return api.SetEdiCompany204Response{}, s.opts.EDI.SetPartyID(ctx, req.Body.PartyId)
}

func (s *Server) SetDepotEdiLocation(ctx context.Context, req api.SetDepotEdiLocationRequestObject) (api.SetDepotEdiLocationResponseObject, error) {
	if !depotAllowed(ctx, req.Id) {
		return nil, edi.ErrNotFound
	}
	return api.SetDepotEdiLocation204Response{}, s.opts.EDI.SetDepotLocation(ctx, req.Id, req.Body.Location)
}

// postmarkInbound is POST /hooks/postmark/inbound: no session, basic auth
// postmark:<secret>. A mail to an unknown address answers 200 like any
// other, so the answer never tells which addresses exist.
func (s *Server) postmarkInbound(w http.ResponseWriter, r *http.Request) {
	user, pw, ok := r.BasicAuth()
	// Hashing first makes the comparison constant-time in the length too.
	hu, hp, want := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(pw)), sha256.Sum256([]byte(s.opts.InboundSecret))
	hpm := sha256.Sum256([]byte("postmark"))
	if subtle.ConstantTimeCompare(hu[:], hpm[:])&subtle.ConstantTimeCompare(hp[:], want[:]) != 1 || !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="inbound"`)
		writeError(w, http.StatusUnauthorized, "E4010", "bad credentials")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInbound))
	if tooBig := new(http.MaxBytesError); errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, "E4130", "the body is over 40 MiB")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "E4001", "the body could not be read")
		return
	}
	in, err := mail.ParsePostmarkInbound(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "E4001", "invalid inbound mail: "+err.Error())
		return
	}
	if err := s.opts.EDI.ReceiveMail(r.Context(), in); err != nil {
		log.Printf("httpapi: inbound mail not stored: %v", err) // Postmark retries a non-2xx answer
		writeError(w, http.StatusInternalServerError, "E9000", "internal error")
		return
	}
	w.WriteHeader(http.StatusOK)
}
