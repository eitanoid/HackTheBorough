//go:build !linux

package subproc

import (
	"os"
	"os/exec"
)

// Portable fallback for development on Windows and macOS: no process groups,
// no privilege drop, no peak-memory figure. The Linux build is the real one.

type cred struct{}

func dropTarget() *cred { return nil }

func prepare(cmd *exec.Cmd, dir string, c *cred) error { return nil }

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func peakRSSKB(ps *os.ProcessState) int64 { return 0 }

func Harden() error { return nil }
