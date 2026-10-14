//go:build !unix

package gitcli

import "os/exec"

// stopTree is the portable fallback for systems without Unix process groups,
// Windows among them: only git itself is killed. A helper it started may
// outlive it; Stream still returns, because cmd.WaitDelay and closeAfter
// bound the wait for the pipes that helper inherited. Windows has no
// SIGTERM, so graceful makes no difference.
func stopTree(cmd *exec.Cmd, _ bool) {
	cmd.Cancel = func() error { return cmd.Process.Kill() }
}

// killGroup has no process group to kill on these systems. A Job Object
// would give Windows the same guarantee as Unix (golang.org/x/sys/windows).
func killGroup(*exec.Cmd) {}
