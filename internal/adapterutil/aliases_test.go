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
