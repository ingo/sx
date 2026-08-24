//go:build unix

package theme

import (
	"os"

	"golang.org/x/sys/unix"
)

// isForeground reports whether the process is in the terminal's foreground
// process group. A background job that touches terminal modes gets suspended
// by SIGTTOU, so callers must skip terminal queries when this is false.
func isForeground() bool {
	pgrp, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return false
	}
	return pgrp == unix.Getpgrp()
}
