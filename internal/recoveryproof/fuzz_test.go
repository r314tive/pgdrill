package recoveryproof

import (
	"encoding/json"
	"reflect"
	"testing"
)

func FuzzParseObservation(f *testing.F) {
	seed, err := json.Marshal(Observation{
		SchemaVersion:           ObservationSchema,
		ReplayPauseState:        "paused",
		RecoveryTargetTimeline:  "latest",
		RecoveryTargetInclusive: "on",
		RecoveryTargetAction:    "pause",
	})
	if err != nil {
		f.Fatalf("marshal fuzz seed: %v", err)
	}
	f.Add(seed)
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add(append(append([]byte(nil), seed...), []byte(` {}`)...))

	f.Fuzz(func(t *testing.T, data []byte) {
		first, firstErr := ParseObservation(data)
		second, secondErr := ParseObservation(data)
		if (firstErr == nil) != (secondErr == nil) {
			t.Fatalf(
				"ParseObservation() acceptance is not deterministic: first=%v second=%v",
				firstErr,
				secondErr,
			)
		}
		if firstErr != nil {
			return
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatal("ParseObservation() result is not deterministic")
		}
		if first.SchemaVersion != ObservationSchema {
			t.Fatalf("ParseObservation() returned unsupported schema %q", first.SchemaVersion)
		}
		canonical, err := json.Marshal(first)
		if err != nil {
			t.Fatalf("marshal accepted observation: %v", err)
		}
		reparsed, err := ParseObservation(canonical)
		if err != nil {
			t.Fatalf("parse canonical observation: %v", err)
		}
		if !reflect.DeepEqual(reparsed, first) {
			t.Fatal("canonical observation changed parsed value")
		}
	})
}
