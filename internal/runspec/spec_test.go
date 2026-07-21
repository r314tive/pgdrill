package runspec

import (
	"bytes"
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestNewCanonicalizesAndHashesEquivalentSpecs(t *testing.T) {
	first := validDocument()
	first.Target.Spec.Labels = map[string]string{"zone": "a", "environment": "test"}
	first.RecoveryTarget = model.RecoveryTarget{
		Type:     model.RecoveryTargetTimestamp,
		Value:    "2026-07-21T10:30:00+05:00",
		Timeline: "0002",
	}
	second := validDocument()
	second.Target.Spec.Labels = map[string]string{"environment": "test", "zone": "a"}
	second.RecoveryTarget = model.RecoveryTarget{
		Type:     model.RecoveryTargetTimestamp,
		Value:    "2026-07-21T05:30:00Z",
		Timeline: "2",
	}

	firstSpec, err := New(first)
	if err != nil {
		t.Fatalf("New(first) error = %v", err)
	}
	secondSpec, err := New(second)
	if err != nil {
		t.Fatalf("New(second) error = %v", err)
	}
	if firstSpec.Digest() != secondSpec.Digest() {
		t.Fatalf("equivalent specs have different digests: %q != %q", firstSpec.Digest(), secondSpec.Digest())
	}
	if !bytes.Equal(firstSpec.CanonicalJSON(), secondSpec.CanonicalJSON()) {
		t.Fatalf("equivalent specs have different canonical JSON:\n%s\n%s", firstSpec.CanonicalJSON(), secondSpec.CanonicalJSON())
	}
	if !ValidDigest(firstSpec.Digest()) {
		t.Fatalf("invalid digest %q", firstSpec.Digest())
	}
	if got := firstSpec.Document().RecoveryTarget.Value; got != "2026-07-21T05:30:00Z" {
		t.Fatalf("canonical timestamp = %q", got)
	}
}

func TestSpecOwnsDefensiveCopies(t *testing.T) {
	document := validDocument()
	document.Target.Spec.Labels = map[string]string{"environment": "test"}
	spec, err := New(document)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	wantDigest := spec.Digest()
	wantJSON := string(spec.CanonicalJSON())

	document.Target.Spec.Labels["environment"] = "mutated"
	document.ProbeProfile.Probes[0].Name = "mutated"
	exposed := spec.Document()
	exposed.Target.Spec.Labels["environment"] = "also-mutated"
	exposed.ProbeProfile.Probes[0].Name = "also-mutated"
	canonical := spec.CanonicalJSON()
	canonical[0] = '['

	if spec.Digest() != wantDigest || string(spec.CanonicalJSON()) != wantJSON {
		t.Fatalf("immutable spec changed after caller mutation: digest=%q json=%s", spec.Digest(), spec.CanonicalJSON())
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate() error after caller mutation = %v", err)
	}
}

func TestDigestChangesWithExecutionIntent(t *testing.T) {
	base, err := New(validDocument())
	if err != nil {
		t.Fatalf("New(base) error = %v", err)
	}

	changedDocument := validDocument()
	changedDocument.Source.Ref.Revision = "sha256:" + strings.Repeat("b", 64)
	changed, err := New(changedDocument)
	if err != nil {
		t.Fatalf("New(changed) error = %v", err)
	}
	if base.Digest() == changed.Digest() {
		t.Fatalf("different source revisions produced digest %q", base.Digest())
	}
}

func TestNewRejectsInvalidContracts(t *testing.T) {
	tests := []struct {
		name string
		edit func(*model.DrillSpec)
		want string
	}{
		{name: "schema", edit: func(d *model.DrillSpec) { d.SchemaVersion = "future" }, want: "schema_version"},
		{name: "mode", edit: func(d *model.DrillSpec) { d.Mode = "future" }, want: "drill mode"},
		{name: "source revision", edit: func(d *model.DrillSpec) { d.Source.Ref.Revision = "" }, want: "source.ref.revision"},
		{name: "native provider", edit: func(d *model.DrillSpec) { d.Source.Provider = "" }, want: "source provider"},
		{name: "source driver mismatch", edit: func(d *model.DrillSpec) { d.Source.Ref.Driver = "barman" }, want: "does not match provider"},
		{name: "selection id", edit: func(d *model.DrillSpec) {
			d.BackupSelection = model.BackupSelection{Type: model.BackupSelectionByID}
		}, want: "backup_selection.backup_id"},
		{name: "target driver mismatch", edit: func(d *model.DrillSpec) { d.Target.Ref.Driver = "container" }, want: "does not match target type"},
		{name: "recovery target", edit: func(d *model.DrillSpec) {
			d.RecoveryTarget = model.RecoveryTarget{Type: model.RecoveryTargetTimestamp, Value: "not-a-time"}
		}, want: "invalid recovery target"},
		{name: "empty probes", edit: func(d *model.DrillSpec) { d.ProbeProfile.Probes = nil }, want: "at least one probe"},
		{name: "duplicate probe", edit: func(d *model.DrillSpec) {
			d.ProbeProfile.Probes = append(d.ProbeProfile.Probes, d.ProbeProfile.Probes[0])
		}, want: "duplicate probe name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document := validDocument()
			tt.edit(&document)
			_, err := New(document)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSnapshotPreservesInvalidRequestForEngineValidation(t *testing.T) {
	document := validDocument()
	document.RecoveryTarget = model.RecoveryTarget{Type: model.RecoveryTargetTimestamp, Value: "invalid"}
	spec, err := Snapshot(document)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "invalid recovery target") {
		t.Fatalf("Validate() error = %v", err)
	}
	if !ValidDigest(spec.Digest()) {
		t.Fatalf("snapshot digest %q is invalid", spec.Digest())
	}
}

func TestParseRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	spec, err := New(validDocument())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	unknown := bytes.Replace(spec.CanonicalJSON(), []byte(`"mode":"native"`), []byte(`"mode":"native","unknown":true`), 1)
	if _, err := Parse(unknown); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Parse(unknown) error = %v", err)
	}
	if _, err := Parse(append(spec.CanonicalJSON(), []byte(` {}`)...)); err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("Parse(trailing) error = %v", err)
	}
}

func validDocument() model.DrillSpec {
	return model.DrillSpec{
		Mode:    model.DrillModeNative,
		Cluster: "production-main",
		Source: model.BackupSourceSpec{
			Ref: model.ComponentRef{
				ID:       "production-main",
				Driver:   "wal-g",
				Revision: "sha256:" + strings.Repeat("a", 64),
			},
			Provider: model.ProviderWALG,
		},
		BackupSelection: model.BackupSelection{Type: model.BackupSelectionLatestAvailable},
		Target: model.RestoreTargetSpec{
			Ref: model.ComponentRef{
				ID:       "/var/tmp/pgdrill/production-main",
				Driver:   "local",
				Revision: "sha256:" + strings.Repeat("c", 64),
			},
			Spec: model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/var/tmp/pgdrill/production-main"},
		},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		ProbeProfile: model.ProbeProfileSpec{
			Ref: model.ComponentRef{
				ID:       "production-main/inline",
				Driver:   "inline",
				Revision: "sha256:" + strings.Repeat("d", 64),
			},
			Probes: []model.ProbeDescriptor{{Type: model.ProbeSQL, Name: "select_1"}},
		},
	}
}
