package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// File writes each mail as an .eml into Dir (dev/test driver).
type File struct{ dir, from string }

func NewFile(dir, from string) *File { return &File{dir: dir, from: from} }

func (f *File) Name() string { return "file" }

// hdr strips CR/LF so values cannot inject headers.
func hdr(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

func (f *File) Send(ctx context.Context, m Outgoing) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	var h bytes.Buffer
	to := make([]string, len(m.To))
	for i, t := range m.To {
		to[i] = hdr(t)
	}
	fmt.Fprintf(&h, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=%s\r\n\r\n",
		hdr(f.from), strings.Join(to, ", "), mime.QEncoding.Encode("utf-8", hdr(m.Subject)),
		time.Now().UTC().Format(time.RFC1123Z), mw.Boundary())

	tp, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}, "Content-Transfer-Encoding": {"8bit"}})
	if err != nil {
		return err
	}
	tp.Write([]byte(m.Text))
	for _, a := range m.Attachments {
		ct := hdr(a.ContentType)
		if ct == "" {
			ct = "application/octet-stream"
		}
		ap, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {mime.FormatMediaType(ct, map[string]string{"name": hdr(a.Name)})},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": hdr(a.Name)})},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return err
		}
		enc := base64.StdEncoding.EncodeToString(a.Data)
		for len(enc) > 76 {
			ap.Write([]byte(enc[:76] + "\r\n"))
			enc = enc[76:]
		}
		ap.Write([]byte(enc + "\r\n"))
	}
	if err := mw.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		return err
	}
	name := time.Now().UTC().Format("20060102T150405") + "-" + ulid.Make().String() + ".eml"
	return os.WriteFile(filepath.Join(f.dir, name), append(h.Bytes(), b.Bytes()...), 0o644)
}
