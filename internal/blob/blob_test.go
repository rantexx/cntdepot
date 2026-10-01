package blob

import (
	"bytes"
	"errors"
	"testing"
)

func TestPutGetRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("photo bytes")
	d1, err := s.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := s.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 || len(d1) != 64 {
		t.Fatalf("digests %q, %q; want the same 64-char digest", d1, d2)
	}
	got, err := s.Get(d1)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestGetRejectsBadAndMissingDigests(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("../../etc/passwd"); !errors.Is(err, ErrBadDigest) {
		t.Fatalf("err = %v; want ErrBadDigest", err)
	}
	missing := "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := s.Get(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v; want ErrNotFound", err)
	}
}
