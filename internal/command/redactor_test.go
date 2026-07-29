package command

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

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
	if got, want := len(redactor.values), 2; got != want {
		t.Fatalf("len(Values) = %d, want %d", got, want)
	}
}

func TestRedactorValidationRejectsUnboundedConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		redactor Redactor
		want     string
	}{
		{
			name:     "invalid replacement",
			redactor: NewRedactor().withReplacement(string([]byte{0xff})),
			want:     "replacement must be valid UTF-8",
		},
		{
			name:     "oversized replacement",
			redactor: NewRedactor().withReplacement(strings.Repeat("x", maxRedactionReplacementBytes+1)),
			want:     "replacement exceeds",
		},
		{
			name:     "oversized value",
			redactor: NewRedactor(strings.Repeat("x", maxRedactionValueBytes+1)),
			want:     "value 0 exceeds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.redactor.validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRedactorDoesNotCompileMatcherBeforeBoundsValidation(t *testing.T) {
	values := make([]string, maxRedactionValues+1)
	for index := range values {
		values[index] = fmt.Sprintf("secret-%d", index)
	}

	redactor := NewRedactor(values...)

	if redactor.compiled != nil {
		t.Fatal("oversized redactor compiled a matcher before validation")
	}
	if err := redactor.validate(); err == nil ||
		!strings.Contains(err.Error(), "values exceed maximum count") {
		t.Fatalf("validate() error = %v", err)
	}
	if got := redactor.RedactString("must-not-leak"); got != "" {
		t.Fatalf("invalid redactor did not fail closed: %q", got)
	}
}

func TestRedactorFailsClosedWhenReplacementContainsSecret(t *testing.T) {
	redactor := NewRedactor("REDACT")

	if err := redactor.validate(); err == nil ||
		!strings.Contains(err.Error(), "replacement must not contain redaction value") {
		t.Fatalf("validate() error = %v", err)
	}
	if got := redactor.RedactString("before-REDACT-after"); got != "" {
		t.Fatalf("invalid redactor retained durable text: %q", got)
	}
	if err := RedactError(errors.New("before-REDACT-after"), "REDACT"); err == nil || err.Error() != "" {
		t.Fatalf("RedactError() = %v, want empty fail-closed message", err)
	}
}

func TestRedactorValidationRejectsReplacementContainingSecret(t *testing.T) {
	redactor := NewRedactor("credential").withReplacement("[credential hidden]")

	if err := redactor.validate(); err == nil ||
		!strings.Contains(err.Error(), "replacement must not contain redaction value") {
		t.Fatalf("validate() error = %v", err)
	}
}

func TestRedactorProtectsSecretPrefixAtTruncationBoundary(t *testing.T) {
	redactor := NewRedactor("secret-value")

	if got, want := redactor.redactTruncatedSuffix("prefix secret-va"), "prefix [REDACTED]"; got != want {
		t.Fatalf("redactTruncatedSuffix() = %q, want %q", got, want)
	}
	if got, want := redactor.redactTruncatedSuffix("unrelated"), "unrelated"; got != want {
		t.Fatalf("redactTruncatedSuffix(unrelated) = %q, want %q", got, want)
	}
}

func TestRedactorReusesCompiledMatcher(t *testing.T) {
	redactor := NewRedactor("first", "second")
	compiled := redactor.compiled

	for range 10 {
		_ = redactor.RedactString("first second")
	}
	if redactor.compiled != compiled {
		t.Fatal("RedactString() rebuilt the compiled matcher")
	}
}

func TestSensitiveEnvironmentNamesIncludeConnectionEndpoints(t *testing.T) {
	for _, name := range []string{
		"DATABASE_URL",
		"AZURE_STORAGE_CONNECTION_STRING",
		"PG_CONNINFO",
		"BACKUP_DSN",
		"SERVICE_URI",
	} {
		if !IsSensitiveEnvName(name) {
			t.Fatalf("IsSensitiveEnvName(%q) = false", name)
		}
	}
	if IsSensitiveEnvName("WALG_FILE_PREFIX") {
		t.Fatal("WALG_FILE_PREFIX was classified as sensitive")
	}
}

func TestRedactErrorDoesNotExposeRawCause(t *testing.T) {
	const secret = "error-secret"
	err := RedactError(fmt.Errorf("provider failed with %s", secret), secret)

	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), defaultReplacement) {
		t.Fatalf("RedactError() = %q", err)
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("RedactError() exposed raw cause: %v", errors.Unwrap(err))
	}
}
