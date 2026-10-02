//go:build !windows

package live

import (
	"os"
	"os/exec"
)

// hideWindow is only needed on Windows.
func hideWindow(*exec.Cmd) {}

// keepWithHub does nothing outside Windows, where the kit does not run: there
// a go2rtc can outlive a hub that is killed hard (still on 127.0.0.1 only,
// behind its password).
func keepWithHub(*os.Process) error { return nil }
