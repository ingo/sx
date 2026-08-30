package git

import (
	"context"
	"runtime/debug"
)

// Availability reports whether git operations can run. Kept for API
// compatibility with callers written when this package shelled out to a
// system git binary — with go-git embedded, there is no external binary to
// probe for, so this always reports available.
type Availability struct {
	Available bool
	Version   string
	Reason    string
}

// CheckAvailability always reports git as available: operations go through
// go-git, an embedded pure-Go implementation, not a system git binary users
// would need to install. Version reports the embedded go-git module version
// when build info is present (always true for a compiled binary; absent
// only when running via `go run`).
func CheckAvailability(ctx context.Context) Availability {
	version := "embedded"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/go-git/go-git/v5" {
				version = "go-git " + dep.Version
				break
			}
		}
	}
	return Availability{Available: true, Version: version}
}
