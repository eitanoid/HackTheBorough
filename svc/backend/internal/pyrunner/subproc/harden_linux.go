//go:build linux

package subproc

import "syscall"

const prSetDumpable = 4 // PR_SET_DUMPABLE

// Harden marks the backend process non-dumpable, so a same-user child cannot
// read /proc/<backend>/environ or /proc/<backend>/mem. Call it once at
// start-up, before any job runs.
func Harden() error {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
