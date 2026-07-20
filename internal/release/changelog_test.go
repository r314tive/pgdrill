package release

import (
	"strings"
	"testing"
)

func TestExtractChangelog(t *testing.T) {
	changelog := `# Changelog

## [Unreleased]

### Added

- Future change.

## [0.1.0-alpha.6] - 2026-07-20

### Added

- Release change.

### Fixed

- Release fix.

## [0.1.0-alpha.5] - 2026-07-19

- Old change.
`
	notes, err := ExtractChangelog(strings.NewReader(changelog), "v0.1.0-alpha.6")
	if err != nil {
		t.Fatalf("extract changelog: %v", err)
	}
	want := "### Added\n\n- Release change.\n\n### Fixed\n\n- Release fix.\n"
	if notes != want {
		t.Fatalf("unexpected notes:\n%s", notes)
	}
}

func TestExtractChangelogRejectsMissingOrEmptyEntry(t *testing.T) {
	for name, changelog := range map[string]string{
		"missing": "## [0.1.0-alpha.5]\n\n- Old.\n",
		"empty":   "## [0.1.0-alpha.6]\n\n## [0.1.0-alpha.5]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ExtractChangelog(strings.NewReader(changelog), "v0.1.0-alpha.6"); err == nil {
				t.Fatal("expected changelog extraction to fail")
			}
		})
	}
}
