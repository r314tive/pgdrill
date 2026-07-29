package runspec

import (
	"encoding/json"
	"testing"
)

func FuzzParse(f *testing.F) {
	valid, err := json.Marshal(validDocument())
	if err != nil {
		f.Fatalf("marshal fuzz seed: %v", err)
	}
	f.Add(valid)
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add(append(append([]byte(nil), valid...), []byte(` {}`)...))

	f.Fuzz(func(t *testing.T, data []byte) {
		first, firstErr := Parse(data)
		second, secondErr := Parse(data)
		if (firstErr == nil) != (secondErr == nil) {
			t.Fatalf("Parse() acceptance is not deterministic: first=%v second=%v", firstErr, secondErr)
		}
		if firstErr != nil {
			return
		}
		if err := first.Validate(); err != nil {
			t.Fatalf("Parse() returned invalid spec: %v", err)
		}
		if first.Digest() != second.Digest() ||
			string(first.CanonicalJSON()) != string(second.CanonicalJSON()) {
			t.Fatal("Parse() result is not deterministic")
		}
		reparsed, err := Parse(first.CanonicalJSON())
		if err != nil {
			t.Fatalf("parse canonical JSON: %v", err)
		}
		if reparsed.Digest() != first.Digest() {
			t.Fatal("canonical JSON changed spec digest")
		}
	})
}
