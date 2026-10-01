// Package testdir makes temporary folders for tests. On Windows, SQLite and
// virus scanners can hold a file for a moment after it is closed, which makes
// t.TempDir's cleanup fail with "directory is not empty" although the test
// passed. New retries the removal instead.
package testdir

import (
	"os"
	"testing"
	"time"
)

// New returns a fresh folder that is removed when the test ends. Register it
// before opening anything inside it, so the cleanups that close files run
// first.
func New(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "depot-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var err error
		for range 20 {
			if err = os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("could not remove temporary folder %s: %v", dir, err)
	})
	return dir
}
