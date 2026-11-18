package mail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	netmail "net/mail"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestFileMailerWritesEML(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox", "sub") // must be created
	f := NewFile(dir, "depot@example.com")
	if f.Name() != "file" {
		t.Fatal("name")
	}
	att := []byte{0, 1, 2, 250, 255, 'x'}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ { // concurrent sends must not collide
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := f.Send(context.Background(), Outgoing{
				To: []string{"a@x.com", "b@x.com"}, Subject: "COPARN test", Text: "hello body",
				Attachments: []Attachment{{Name: "a.edi", ContentType: "text/plain", Data: att}},
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	files, _ := filepath.Glob(filepath.Join(dir, "*.eml"))
	if len(files) != 20 {
		t.Fatalf("files=%d", len(files))
	}
	fh, err := os.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	msg, err := netmail.ReadMessage(fh)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("Subject") != "COPARN test" {
		t.Fatal("subject")
	}
	to, _ := msg.Header.AddressList("To")
	if len(to) != 2 || to[0].Address != "a@x.com" || to[1].Address != "b@x.com" {
		t.Fatalf("to=%v", to)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mt, "multipart/") {
		t.Fatal(mt, err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	p1, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := io.ReadAll(p1)
	if strings.TrimSpace(string(b1)) != "hello body" {
		t.Fatalf("text=%q", b1)
	}
	p2, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if p2.FileName() != "a.edi" {
		t.Fatal("filename", p2.FileName())
	}
	raw, _ := io.ReadAll(p2)
	got, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(raw)), ""))
	if err != nil || string(got) != string(att) {
		t.Fatalf("att %v %v", got, err)
	}
}

func TestPostmarkSendsJSON(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/email" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Postmark-Server-Token") != "tok" {
			t.Error("token")
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"ErrorCode":0,"Message":"OK"}`))
	}))
	defer srv.Close()
	p := NewPostmark("tok", "from@x.com", srv.URL)
	if p.Name() != "postmark" {
		t.Fatal("name")
	}
	err := p.Send(context.Background(), Outgoing{
		To: []string{"a@x.com", "b@x.com"}, Subject: "S", Text: "T",
		Attachments: []Attachment{{Name: "f.edi", ContentType: "text/plain", Data: []byte("UNB+")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["From"] != "from@x.com" || got["To"] != "a@x.com,b@x.com" || got["Subject"] != "S" ||
		got["TextBody"] != "T" || got["MessageStream"] != "outbound" {
		t.Fatalf("%v", got)
	}
	a := got["Attachments"].([]any)[0].(map[string]any)
	if a["Name"] != "f.edi" || a["ContentType"] != "text/plain" || a["Content"] != base64.StdEncoding.EncodeToString([]byte("UNB+")) {
		t.Fatalf("%v", a)
	}
}

func TestPostmarkErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		w.Write([]byte(`{"ErrorCode":300,"Message":"Invalid email"}`))
	}))
	defer srv.Close()
	err := NewPostmark("t", "f@x.com", srv.URL).Send(context.Background(), Outgoing{To: []string{"a@x.com"}})
	if err == nil || !strings.Contains(err.Error(), "Invalid email") {
		t.Fatal(err)
	}
}

func TestParseInboundTrust(t *testing.T) {
	mk := func(headers string) []byte {
		return []byte(`{"FromFull":{"Email":"line@carrier.com"},"ToFull":[{"Email":"in@depot.com"}],
"Subject":"COPARN","TextBody":"hi","Headers":[` + headers + `],
"Attachments":[{"Name":"a.edi","ContentType":"text/plain","Content":"` + base64.StdEncoding.EncodeToString([]byte("UNB+x")) + `"}]}`)
	}
	cases := []struct {
		h    string
		want bool
	}{
		{`{"Name":"Received-SPF","Value":"Pass (sender SPF authorized)"}`, true},
		{`{"Name":"Authentication-Results","Value":"mx; spf=none; dkim=pass header.d=carrier.com"}`, true},
		{`{"Name":"X-Spam-Tests","Value":"DKIM_SIGNED,SPF_PASS"}`, true},
		{`{"Name":"X-Spam-Tests","Value":"DKIM_SIGNED,DKIM_VALID_AU"}`, true},
		{`{"Name":"X-Spam-Tests","Value":"DKIM_SIGNED,DKIM_VALID"}`, false},
		{`{"Name":"Received-SPF","Value":"Fail"},{"Name":"X-Other","Value":"dkim=pass"}`, false},
	}
	for i, c := range cases {
		in, err := ParsePostmarkInbound(mk(c.h))
		if err != nil {
			t.Fatal(err)
		}
		if in.Trusted != c.want {
			t.Errorf("case %d trusted=%v", i, in.Trusted)
		}
		if in.From != "line@carrier.com" || len(in.To) != 1 || in.To[0] != "in@depot.com" || in.Subject != "COPARN" || in.Text != "hi" {
			t.Errorf("%+v", in)
		}
		if len(in.Attachments) != 1 || string(in.Attachments[0].Data) != "UNB+x" || in.Attachments[0].Name != "a.edi" {
			t.Errorf("att %+v", in.Attachments)
		}
	}
	if _, err := ParsePostmarkInbound([]byte("nope")); err == nil {
		t.Fatal("want error")
	}
}
