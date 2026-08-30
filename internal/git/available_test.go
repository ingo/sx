package git

import (
	"context"
	"testing"
)

func TestCheckAvailabilityAlwaysAvailable(t *testing.T) {
	// go-git is embedded — there is no system binary that could be missing,
	// unlike when this package shelled out to a system git.
	av := CheckAvailability(context.Background())
	if !av.Available {
		t.Fatalf("git should always be available (embedded via go-git), got reason %q", av.Reason)
	}
	if av.Reason != "" {
		t.Errorf("availability = %+v, want no reason", av)
	}
}
