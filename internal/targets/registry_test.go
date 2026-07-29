package targets

import (
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestNewRestoreTargetBuildsLocalTarget(t *testing.T) {
	target, err := NewRestoreTarget(config.TargetConfig{
		Type: model.RestoreTargetLocal,
	})

	if err != nil {
		t.Fatalf("NewRestoreTarget() error = %v", err)
	}
	if target == nil || target.Type() != model.RestoreTargetLocal {
		t.Fatalf("NewRestoreTarget() = %#v", target)
	}
}

func TestNewRestoreTargetRejectsUnimplementedType(t *testing.T) {
	target, err := NewRestoreTarget(config.TargetConfig{
		Type: model.RestoreTargetContainer,
	})

	if err == nil || !strings.Contains(err.Error(), "is not implemented") {
		t.Fatalf("NewRestoreTarget() = %#v, %v", target, err)
	}
	if target != nil {
		t.Fatalf("NewRestoreTarget() returned target on error: %#v", target)
	}
}
