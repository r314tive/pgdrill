package adapterutil

import (
	"strings"
	"testing"
)

func TestOptionalStringAliasRejectsConflictingValues(t *testing.T) {
	_, _, err := OptionalStringAlias(
		map[string]any{"backup_id": "first", "id": "second"},
		"backup id",
		"backup_id",
		"id",
	)

	if err == nil || !strings.Contains(err.Error(), `aliases "backup_id" and "id" conflict`) {
		t.Fatalf("OptionalStringAlias() error = %v", err)
	}
	if strings.Contains(err.Error(), "first") || strings.Contains(err.Error(), "second") {
		t.Fatalf("OptionalStringAlias() leaked conflicting values: %v", err)
	}
}

func TestOptionalStringAliasAcceptsEquivalentTrimmedValues(t *testing.T) {
	value, found, err := OptionalTrimmedStringAlias(
		map[string]any{"backup_id": " same ", "id": "same"},
		"backup id",
		"backup_id",
		"id",
	)

	if err != nil || !found || value != "same" {
		t.Fatalf("OptionalTrimmedStringAlias() = %q, %t, %v", value, found, err)
	}
}

func TestRequiredStringAliases(t *testing.T) {
	raw, err := RequiredStringAlias(
		map[string]any{"backup_id": " value "},
		"backup id",
		"backup_id",
	)
	if err != nil || raw != " value " {
		t.Fatalf("RequiredStringAlias() = %q, %v", raw, err)
	}

	trimmed, err := RequiredTrimmedStringAlias(
		map[string]any{"backup_id": " value "},
		"backup id",
		"backup_id",
	)
	if err != nil || trimmed != "value" {
		t.Fatalf("RequiredTrimmedStringAlias() = %q, %v", trimmed, err)
	}
}

func TestRequiredStringAliasRejectsMissingOrBlankValues(t *testing.T) {
	for name, object := range map[string]map[string]any{
		"missing": {},
		"blank":   {"backup_id": " \t "},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RequiredStringAlias(object, "backup id", "backup_id")
			if err == nil || err.Error() != "missing backup id" {
				t.Fatalf("RequiredStringAlias() error = %v", err)
			}
		})
	}
}

func TestOptionalBoolAliasRejectsConflictingValues(t *testing.T) {
	_, _, err := OptionalBoolAlias(
		map[string]any{"is_permanent": true, "permanent": false},
		"permanent",
		"is_permanent",
		"permanent",
	)

	if err == nil || !strings.Contains(err.Error(), "aliases") {
		t.Fatalf("OptionalBoolAlias() error = %v", err)
	}
}
