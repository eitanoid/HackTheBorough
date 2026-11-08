//go:build linux

package subproc

import (
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

// cred is the identity jobs run as when the backend is root.
type cred struct{ uid, gid uint32 }

// dropTarget returns the unprivileged identity to run jobs as, or nil when the
// backend is not root and jobs must run as the backend's own user.
func dropTarget() *cred {
	if os.Geteuid() != 0 {
		return nil
	}
	c := &cred{uid: 65534, gid: 65534}
	if u, err := user.Lookup("nobody"); err == nil {
		if uid, err := strconv.ParseUint(u.Uid, 10, 32); err == nil {
			c.uid = uint32(uid)
		}
		if gid, err := strconv.ParseUint(u.Gid, 10, 32); err == nil {
			c.gid = uint32(gid)
		}
	}
	return c
}

// prepare puts the child in its own process group and, when c is set, runs it
// as that user with dir (its temp directory) handed over.
func prepare(cmd *exec.Cmd, dir string, c *cred) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if c == nil {
		return nil
	}
	if err := os.Chown(dir, int(c.uid), int(c.gid)); err != nil {
		return err
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: c.uid, Gid: c.gid}
	return nil
}

// killGroup kills the child and everything it started.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// peakRSSKB is the child's peak resident memory in KB (0 if unknown).
func peakRSSKB(ps *os.ProcessState) int64 {
	if ps == nil {
		return 0
	}
	if ru, ok := ps.SysUsage().(*syscall.Rusage); ok {
		return int64(ru.Maxrss)
	}
	return 0
}
