package command

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzEvidenceOutput(f *testing.F) {
	f.Add([]byte("prefix secret suffix"), "secret", uint16(8))
	f.Add([]byte{0xff, 0xfe, 'x'}, "", uint16(2))
	f.Add([]byte("valid utf8 \xe2\x98\x83"), "missing", uint16(11))

	f.Fuzz(func(t *testing.T, data []byte, secret string, rawLimit uint16) {
		limit := int64(rawLimit) + 1
		redactor := NewRedactor(secret)
		output, _ := evidenceOutput(data, false, limit, redactor)
		if int64(len(output)) > limit {
			t.Fatalf("evidence output length = %d, limit = %d", len(output), limit)
		}
		if !utf8.ValidString(output) {
			t.Fatalf("evidence output is not valid UTF-8: %q", output)
		}
		if safeFuzzSecret(secret) && !strings.Contains(defaultReplacement, secret) &&
			strings.Contains(string(data), secret) && strings.Contains(output, secret) {
			t.Fatalf("evidence output retained secret %q", secret)
		}
	})
}

func safeFuzzSecret(value string) bool {
	if len(value) < 4 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character > 127 ||
			(character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') {
			return false
		}
	}
	return true
}
