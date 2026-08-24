//go:build !unix

package theme

// isForeground reports whether the process may safely query the terminal.
// Job-control suspension (SIGTTOU) does not exist outside unix, so always
// allow it.
func isForeground() bool {
	return true
}
