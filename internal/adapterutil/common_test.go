package adapterutil

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestCommandEvidenceUsesCommandCompletionTime(t *testing.T) {
	finishedAt := time.Date(2026, time.July, 29, 10, 11, 12, 13, time.UTC)
	command := model.CommandEvidence{FinishedAt: finishedAt}

	record := CommandEvidence(model.ProviderWALG, "backup-list", command)

	if record.ID != "wal-g:backup-list:"+finishedAt.Format(time.RFC3339Nano) {
		t.Fatalf("CommandEvidence() id = %q", record.ID)
	}
	if record.Kind != model.EvidenceCommand ||
		record.Source != string(model.ProviderWALG) ||
		!record.CollectedAt.Equal(finishedAt) ||
		record.Command == nil ||
		!reflect.DeepEqual(*record.Command, command) ||
		record.Attributes["operation"] != "backup-list" {
		t.Fatalf("CommandEvidence() = %#v", record)
	}
}

func TestCommandEvidenceSuppliesMissingCompletionTime(t *testing.T) {
	before := time.Now().UTC()
	record := CommandEvidence(model.ProviderBarman, "list-backups", model.CommandEvidence{})
	after := time.Now().UTC()

	if record.CollectedAt.Before(before) || record.CollectedAt.After(after) {
		t.Fatalf("CommandEvidence() collected_at = %s, want within [%s, %s]", record.CollectedAt, before, after)
	}
	if !strings.HasSuffix(record.ID, record.CollectedAt.Format(time.RFC3339Nano)) {
		t.Fatalf("CommandEvidence() id = %q", record.ID)
	}
}

func TestPlanEvidence(t *testing.T) {
	before := time.Now().UTC()
	record := PlanEvidence(model.ProviderPGBackRest, "restore-plan")
	after := time.Now().UTC()

	if record.Kind != model.EvidencePlan ||
		record.Source != string(model.ProviderPGBackRest) ||
		record.Attributes["operation"] != "restore-plan" {
		t.Fatalf("PlanEvidence() = %#v", record)
	}
	if record.CollectedAt.Before(before) || record.CollectedAt.After(after) {
		t.Fatalf("PlanEvidence() collected_at = %s, want within [%s, %s]", record.CollectedAt, before, after)
	}
	if record.ID != "pgbackrest:restore-plan:"+record.CollectedAt.Format(time.RFC3339Nano) {
		t.Fatalf("PlanEvidence() id = %q", record.ID)
	}
}

func TestCommonValueHelpers(t *testing.T) {
	source := map[string]string{"key": "value"}
	clone := CloneStringMap(source)
	clone["key"] = "changed"
	if source["key"] != "value" {
		t.Fatalf("CloneStringMap() aliased its input")
	}

	if CloneStringMap(nil) != nil {
		t.Fatalf("CloneStringMap(nil) is not nil")
	}
	if got := DurationString(0); got != "" {
		t.Fatalf("DurationString(0) = %q", got)
	}
	if got := DurationString(3 * time.Second); got != "3s" {
		t.Fatalf("DurationString(3s) = %q", got)
	}
	if got := FirstNonEmpty("", "value", "later"); got != "value" {
		t.Fatalf("FirstNonEmpty() = %q", got)
	}
	if got := FirstNonEmpty("", ""); got != "" {
		t.Fatalf("FirstNonEmpty(empty) = %q", got)
	}
	if StringMapOrNil(nil) != nil {
		t.Fatalf("StringMapOrNil(nil) is not nil")
	}
	if got := StringMapOrNil(source); !reflect.DeepEqual(got, source) {
		t.Fatalf("StringMapOrNil() = %#v", got)
	}
}
