package adapters

import (
	"testing"

	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestNewProviderBuildsPGProbackupAdapter(t *testing.T) {
	provider, err := NewProvider(config.ProviderConfig{
		Type:      model.ProviderPGProbackup,
		BackupDir: "/srv/pg_probackup",
		Instance:  "main",
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	if provider.Type() != model.ProviderPGProbackup {
		t.Fatalf("unexpected provider type %q", provider.Type())
	}
}
