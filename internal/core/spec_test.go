package core

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/checkpoint"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/runspec"
)

func TestEnginesRejectLegacySpecBeforeLifecycleOrExternalWork(t *testing.T) {
	native := nativeRequest(
		model.ProviderWALG,
		model.TargetSpec{Type: model.RestoreTargetLocal},
		model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	)
	nativeDocument := native.Spec.Document()
	nativeDocument.SchemaVersion = model.LegacyDrillSpecSchemaVersion
	legacyNative, err := runspec.New(nativeDocument)
	if err != nil {
		t.Fatalf("runspec.New(legacy native) error = %v", err)
	}
	native.Spec = legacyNative
	provider := &fakeProvider{catalog: model.BackupCatalog{Provider: model.ProviderWALG}}
	target := &fakeTarget{}
	nativeSink := &fakeSink{}
	result, err := (Engine{
		Source:           provider,
		CatalogValidator: provider,
		Planner:          provider,
		Target:           target,
		Probes:           []Probe{passingProbe()},
		Checkpoints:      checkpoint.NewMemoryStore(),
		Sink:             nativeSink,
	}).Run(context.Background(), native)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Engine.Run() error = %v", err)
	}
	if !reflect.DeepEqual(result, model.DrillResult{}) ||
		nativeSink.called ||
		len(provider.calls) != 0 ||
		len(target.calls) != 0 {
		t.Fatalf(
			"legacy native spec crossed execution boundary: result=%#v sink=%t provider=%#v target=%#v",
			result,
			nativeSink.called,
			provider.calls,
			target.calls,
		)
	}

	managed := managedRequest("legacy-managed")
	managedDocument := managed.Spec.Document()
	managedDocument.SchemaVersion = model.LegacyDrillSpecSchemaVersion
	legacyManaged, err := runspec.New(managedDocument)
	if err != nil {
		t.Fatalf("runspec.New(legacy managed) error = %v", err)
	}
	managed.Spec = legacyManaged
	resolver := &fakeManagedResolver{
		resolution: managedResolution(&fakeManagedTarget{}, &fakePostRestoreChecker{}),
	}
	managedSink := &fakeSink{}
	result, err = (ManagedEngine{
		Resolver:    resolver,
		Checkpoints: checkpoint.NewMemoryStore(),
		Sink:        managedSink,
	}).Run(context.Background(), managed)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("ManagedEngine.Run() error = %v", err)
	}
	if !reflect.DeepEqual(result, model.DrillResult{}) || managedSink.called || resolver.calls != 0 {
		t.Fatalf(
			"legacy managed spec crossed execution boundary: result=%#v sink=%t resolver=%d",
			result,
			managedSink.called,
			resolver.calls,
		)
	}
}
