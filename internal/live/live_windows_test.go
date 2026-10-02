//go:build windows

package live

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// sleeper is a child process that keeps running for a minute.
func sleeper() *exec.Cmd {
	return exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE"), "-n", "60", "127.0.0.1")
}

func TestJobObjectEndsGo2rtcWhenTheHubsHandleCloses(t *testing.T) {
	cmd := sleeper()
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	job, err := newKillOnCloseJob()
	if err != nil {
		t.Fatal(err)
	}
	if err := assignToJob(job, cmd.Process); err != nil {
		_ = windows.CloseHandle(job)
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		t.Fatalf("child ended before the job closed: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	_ = windows.CloseHandle(job) // what Windows does when the hub's process ends
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("child still running after the job object closed")
	}
}

func TestKeepWithHubUsesOneJobForEveryGo2rtc(t *testing.T) {
	var jobs []windows.Handle
	for range 2 {
		cmd := sleeper()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		if err := keepWithHub(cmd.Process); err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, hubJob.handle)
	}
	if jobs[0] == 0 || jobs[0] != jobs[1] {
		t.Fatalf("jobs = %v; want one job object", jobs)
	}
}
