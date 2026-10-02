//go:build windows

package live

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// hideWindow starts go2rtc without a console window of its own.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// hubJob is a Windows job object that ends every process in it when its last
// handle closes. Only the hub holds the handle and never closes it, so Windows
// ends go2rtc however the hub ends: Ctrl+C, End task, or a crash.
var hubJob struct {
	once   sync.Once
	handle windows.Handle
	err    error
}

// keepWithHub puts a started go2rtc into the hub's job object.
func keepWithHub(p *os.Process) error {
	hubJob.once.Do(func() { hubJob.handle, hubJob.err = newKillOnCloseJob() })
	if hubJob.err != nil {
		return fmt.Errorf("job object: %w", hubJob.err)
	}
	return assignToJob(hubJob.handle, p)
}

func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func assignToJob(job windows.Handle, p *os.Process) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return fmt.Errorf("open go2rtc process: %w", err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		return fmt.Errorf("add go2rtc to the job object: %w", err)
	}
	return nil
}
