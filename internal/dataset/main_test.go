package dataset

import (
	"os"
	"testing"

	"depot/internal/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }
