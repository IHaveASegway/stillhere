//go:build unix

package gitcli

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// stopTree starts git in its own process group and, when the command's
// context ends, signals the whole group, so helpers git started (a remote
// helper, a textconv or smudge filter) die with it instead of outliving the
// run. Analysis gets SIGKILL: it is read-only and holds no locks. Sync gets
// SIGTERM, which lets git remove the ref and pack lock files a killed fetch
// would leave behind; os/exec escalates to SIGKILL after cmd.WaitDelay.
func stopTree(cmd *exec.Cmd, graceful bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	sig := syscall.SIGKILL
	if graceful {
		sig = syscall.SIGTERM
	}
	cmd.Cancel = func() error {
		// A negative pid addresses the process group whose id is the pid.
		err := syscall.Kill(-cmd.Process.Pid, sig)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
