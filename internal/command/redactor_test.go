package command

import "testing"

func TestRedactorReplacesLongestOverlappingValueFirst(t *testing.T) {
	redactor := NewRedactor("token", "token-with-suffix")

	if got, want := redactor.RedactString("token-with-suffix"), "[REDACTED]"; got != want {
		t.Fatalf("RedactString() = %q, want %q", got, want)
	}
}

func TestRedactorWithValuesCompactsAndOrdersAllValues(t *testing.T) {
	redactor := NewRedactor("short").WithValues("short-and-long", "short")

	if got, want := redactor.RedactString("short-and-long short"), "[REDACTED] [REDACTED]"; got != want {
		t.Fatalf("RedactString() = %q, want %q", got, want)
	}
	if got, want := len(redactor.Values), 2; got != want {
		t.Fatalf("len(Values) = %d, want %d", got, want)
	}
}
