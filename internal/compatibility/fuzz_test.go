package compatibility

import (
	"os"
	"testing"
)

func FuzzParse(f *testing.F) {
	seed, err := os.ReadFile("../../compatibility/matrix.yaml")
	if err != nil {
		f.Fatalf("read fuzz seed: %v", err)
	}
	f.Add(seed)
	f.Add([]byte(`schema_version: pgdrill.compatibility-matrix/v1`))
	f.Add([]byte("---\n{}\n---\n{}\n"))

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
			t.Fatalf("Parse() returned invalid matrix: %v", err)
		}
		if len(first.Entries) != len(second.Entries) ||
			first.SchemaVersion != second.SchemaVersion ||
			first.UpdatedAt != second.UpdatedAt {
			t.Fatal("Parse() result is not deterministic")
		}
	})
}
