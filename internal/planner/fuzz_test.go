package planner

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func FuzzLoad(f *testing.F) {
	fleet := fixtureFleetForFuzz(f)
	jsonSeed, err := json.Marshal(fleet)
	if err != nil {
		f.Fatalf("marshal fuzz seed: %v", err)
	}
	f.Add(jsonSeed, true)
	f.Add([]byte("schema_version: pgdrill.fleet/v1\n"), false)
	f.Add([]byte(`{} {}`), true)
	f.Add([]byte("---\n{}\n---\n{}\n"), false)

	f.Fuzz(func(t *testing.T, data []byte, useJSON bool) {
		if len(data) > MaxFleetBytes {
			return
		}
		format := "yaml"
		if useJSON {
			format = "json"
		}
		first, firstErr := Load(bytes.NewReader(data), format)
		second, secondErr := Load(bytes.NewReader(data), format)
		if (firstErr == nil) != (secondErr == nil) {
			t.Fatalf("Load() acceptance is not deterministic: first=%v second=%v", firstErr, secondErr)
		}
		if firstErr != nil {
			return
		}
		if err := first.Validate(); err != nil {
			t.Fatalf("Load() returned invalid fleet: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatal("Load() result is not deterministic")
		}
	})
}

func fixtureFleetForFuzz(f *testing.F) Fleet {
	f.Helper()
	fleet, err := LoadFile("testdata/fleet.yaml")
	if err != nil {
		f.Fatalf("load fuzz seed: %v", err)
	}
	return fleet
}
