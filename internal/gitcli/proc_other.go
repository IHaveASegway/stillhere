//go:build !unix

package gitcli

import "os/exec"

// stopTree is the portable fallback for systems without Unix process groups,
// Windows among them: only git itself is killed. A helper it started may
// outlive it briefly; cmd.WaitDelay keeps that helper's inherited pipes from
// hanging Wait. Windows has no SIGTERM, so graceful makes no difference.
func stopTree(cmd *exec.Cmd, _ bool) {
	cmd.Cancel = func() error { return cmd.Process.Kill() }
}
