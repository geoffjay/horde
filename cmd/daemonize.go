package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// daemonize re-executes `horde serve` (without --daemonize) as a detached
// background process and returns immediately, leaving the server running
// detached from the current terminal.
//
// On macOS/Linux this re-execs the binary with setsid so it becomes a new
// session leader, detached from the controlling terminal.
//
//	// bound to daemonize's lifetime would kill it on return.
//
//nolint:noctx // the daemon intentionally outlives this process; a context
func daemonize() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}

	// exe comes from os.Executable() (operator-controlled), not untrusted input.
	cmd := exec.Command(exe, "serve") //#nosec G204
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}

	pid := cmd.Process.Pid
	// Release the process so it keeps running after this parent exits.
	_ = cmd.Process.Release()
	fmt.Fprintf(os.Stderr, "horde server daemonized (pid %d)\n", pid)
	return nil
}
