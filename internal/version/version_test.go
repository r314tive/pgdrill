package version

import "testing"

func TestStringIncludesInjectedReleaseMetadata(t *testing.T) {
	previousVersion, previousCommit, previousDate := Version, Commit, Date
	t.Cleanup(func() {
		Version, Commit, Date = previousVersion, previousCommit, previousDate
	})
	Version = "v1.2.3"
	Commit = "0123456789abcdef"
	Date = "2026-07-29T12:00:00Z"

	if got, want := String(), "pgdrill v1.2.3 (0123456789abcdef, 2026-07-29T12:00:00Z)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
