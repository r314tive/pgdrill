package adapterutil

import (
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestRedactBackupsRejectsSensitiveCanonicalField(t *testing.T) {
	const secret = "catalog-secret"
	result := command.NewRedactor(secret)
	_, err := RedactBackups([]model.Backup{{
		ID:            model.ProviderScopedID(model.ProviderWALG, secret),
		Provider:      model.ProviderWALG,
		ProviderID:    secret,
		ClusterName:   "cluster-" + secret,
		Kind:          model.BackupKindFull,
		Status:        model.BackupStatusAvailable,
		DataDirectory: "/data/" + secret,
		Metadata:      map[string]string{"source": secret},
	}}, result)
	if err == nil || !strings.Contains(err.Error(), `canonical field "provider_id"`) {
		t.Fatalf("RedactBackups() error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("RedactBackups() error leaked configured value: %v", err)
	}
}

func TestRedactBackupsRedactsMetadataWithoutChangingCanonicalIdentity(t *testing.T) {
	const secret = "catalog-secret"
	result := command.NewRedactor(secret)
	original := model.Backup{
		ID:         "wal-g:base-1",
		Provider:   model.ProviderWALG,
		ProviderID: "base-1",
		Kind:       model.BackupKindFull,
		Status:     model.BackupStatusAvailable,
		Metadata:   map[string]string{"source": secret},
	}
	backups, err := RedactBackups([]model.Backup{original}, result)
	if err != nil {
		t.Fatalf("RedactBackups() error = %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("RedactBackups() = %#v", backups)
	}
	if backups[0].ID != original.ID || backups[0].ProviderID != original.ProviderID {
		t.Fatalf("canonical identity changed: %#v", backups[0])
	}
	if strings.Contains(backups[0].Metadata["source"], secret) {
		t.Fatalf("metadata leaked configured value: %#v", backups[0].Metadata)
	}
}

func TestRedactBackupsRejectsSensitiveMetadataKey(t *testing.T) {
	const secret = "catalog-secret"
	result := command.NewRedactor(secret)
	_, err := RedactBackups([]model.Backup{{
		Provider:   model.ProviderWALG,
		ProviderID: "backup",
		Kind:       model.BackupKindFull,
		Status:     model.BackupStatusAvailable,
		Metadata:   map[string]string{"source-" + secret: "value"},
	}}, result)
	if err == nil || !strings.Contains(err.Error(), "structured attribute key") {
		t.Fatalf("RedactBackups() error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("RedactBackups() error leaked configured value: %v", err)
	}
}

func TestRedactCheckRejectsSensitiveCanonicalIdentifiers(t *testing.T) {
	for _, test := range []struct {
		name  string
		check model.Check
		want  string
	}{
		{
			name: "check name",
			check: model.Check{
				Name:   "provider-secret-check",
				Status: model.CheckStatusPassed,
			},
			want: "canonical check name",
		},
		{
			name: "evidence id",
			check: model.Check{
				Name:        "provider-check",
				Status:      model.CheckStatusPassed,
				EvidenceIDs: []string{"evidence-secret-id"},
			},
			want: "canonical check evidence id",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := RedactCheck(test.check, command.NewRedactor("secret"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("RedactCheck() error = %v", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("RedactCheck() leaked configured value: %v", err)
			}
		})
	}
}

func TestRedactCheckRejectsSensitiveAttributeKey(t *testing.T) {
	const secret = "probe-secret"
	_, err := RedactCheck(model.Check{
		Name:       "provider-check",
		Status:     model.CheckStatusPassed,
		Attributes: map[string]string{"source-" + secret: "value"},
	}, command.NewRedactor(secret))
	if err == nil || !strings.Contains(err.Error(), "structured attribute key") {
		t.Fatalf("RedactCheck() error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("RedactCheck() error leaked configured value: %v", err)
	}
}
