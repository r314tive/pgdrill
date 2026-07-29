package report

import (
	"bytes"
	"testing"
)

func FuzzReadJSON(f *testing.F) {
	var seed bytes.Buffer
	if err := WriteJSON(&seed, validTestResult()); err != nil {
		f.Fatalf("write fuzz seed: %v", err)
	}
	f.Add(seed.Bytes())
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add(append(append([]byte(nil), seed.Bytes()...), []byte(` {}`)...))

	f.Fuzz(func(t *testing.T, data []byte) {
		if int64(len(data)) > MaxJSONBytes {
			return
		}
		first, firstErr := ReadJSON(bytes.NewReader(data))
		second, secondErr := ReadJSON(bytes.NewReader(data))
		if (firstErr == nil) != (secondErr == nil) {
			t.Fatalf("ReadJSON() acceptance is not deterministic: first=%v second=%v", firstErr, secondErr)
		}
		if firstErr != nil {
			return
		}
		if err := Validate(first); err != nil {
			t.Fatalf("ReadJSON() returned invalid report: %v", err)
		}
		var canonical bytes.Buffer
		if err := WriteCompatibleJSON(&canonical, first); err != nil {
			t.Fatalf("write accepted report: %v", err)
		}
		reparsed, err := ReadJSON(bytes.NewReader(canonical.Bytes()))
		if err != nil {
			t.Fatalf("read canonical report: %v", err)
		}
		if reparsed.ID != second.ID || reparsed.SpecDigest != second.SpecDigest {
			t.Fatal("canonical report changed identity")
		}
	})
}
