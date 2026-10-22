package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStills(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"b.JPG": "b", "a.jpeg": "a", "notes.txt": "x"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stills, err := loadStills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stills) != 2 || string(stills[0]) != "a" || string(stills[1]) != "b" {
		t.Fatalf("stills = %q, want the two photos in name order", stills)
	}
	if s, err := loadStills(""); s != nil || err != nil {
		t.Fatalf("no folder: got %v, %v", s, err)
	}
	if _, err := loadStills(t.TempDir()); err == nil {
		t.Fatal("a folder without photos should be an error")
	}
}
