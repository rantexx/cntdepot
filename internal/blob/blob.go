// Package blob stores photos under their SHA-256 fingerprint.
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

var (
	ErrNotFound  = errors.New("blob not found")
	ErrBadDigest = errors.New("invalid blob digest")
)

var digestShape = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Store is a folder of content-addressed files.
type Store struct{ dir string }

// Open creates the folder if needed.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Put stores data and returns its digest. Storing the same bytes twice is free.
func (s *Store) Put(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	path := s.path(digest)
	if _, err := os.Stat(path); err == nil {
		return digest, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	// Write to a temporary file and rename, so a crash never leaves a
	// half-written photo under a real digest.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return digest, nil
}

// Get returns the bytes stored under digest.
func (s *Store) Get(digest string) ([]byte, error) {
	if !digestShape.MatchString(digest) {
		return nil, ErrBadDigest
	}
	data, err := os.ReadFile(s.path(digest))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

func (s *Store) path(digest string) string {
	return filepath.Join(s.dir, digest[:2], digest[2:4], digest)
}
