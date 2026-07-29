package command

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	defaultReplacement           = "[REDACTED]"
	maxRedactionValues           = 4096
	maxRedactionValueBytes       = 64 << 10
	maxRedactionValuesTotalBytes = 4 << 20
	maxRedactionReplacementBytes = 1024
)

type Redactor struct {
	replacementValue string
	values           []string
	compiled         *strings.Replacer
	validationErr    error
}

func NewRedactor(values ...string) Redactor {
	return validateCompiledRedactor(compileRedactor(values, defaultReplacement))
}

func (r Redactor) WithValues(values ...string) Redactor {
	if r.validationErr != nil {
		return r
	}
	if len(values) > maxRedactionValues ||
		len(r.values) > maxRedactionValues-len(values) {
		return invalidRedactor(
			r.replacement(),
			fmt.Errorf("values exceed maximum count %d", maxRedactionValues),
		)
	}
	combined := make([]string, 0, len(r.values)+len(values))
	combined = append(combined, r.values...)
	combined = append(combined, values...)
	return validateCompiledRedactor(compileRedactor(combined, r.replacement()))
}

func (r Redactor) withReplacement(replacement string) Redactor {
	return compileRedactor(append([]string{}, r.values...), replacement)
}

func (r Redactor) RedactString(value string) string {
	if r.validationErr != nil {
		return ""
	}
	if len(r.values) == 0 {
		return value
	}
	return r.compiledReplacer().Replace(value)
}

func (r Redactor) writeString(writer io.Writer, value string) error {
	if r.validationErr != nil {
		return nil
	}
	if len(r.values) == 0 {
		_, err := io.WriteString(writer, value)
		return err
	}
	_, err := r.compiledReplacer().WriteString(writer, value)
	return err
}

func (r Redactor) redactTruncatedSuffix(value string) string {
	if r.validationErr != nil {
		return ""
	}
	if value == "" || len(r.values) == 0 {
		return value
	}
	matchedBytes := 0
	for _, secret := range r.values {
		maximum := min(len(value), len(secret))
		for length := maximum; length > matchedBytes; length-- {
			if strings.HasSuffix(value, secret[:length]) {
				matchedBytes = length
				break
			}
		}
	}
	if matchedBytes == 0 {
		return value
	}
	return value[:len(value)-matchedBytes] + r.replacement()
}

func (r Redactor) compiledReplacer() *strings.Replacer {
	if r.compiled != nil {
		return r.compiled
	}
	return buildReplacer(r.values, r.replacement())
}

func (r Redactor) replacement() string {
	if r.replacementValue == "" {
		return defaultReplacement
	}
	return r.replacementValue
}

func (r Redactor) validate() error {
	if r.validationErr != nil {
		return r.validationErr
	}
	replacement := r.replacement()
	if !utf8.ValidString(replacement) {
		return fmt.Errorf("replacement must be valid UTF-8")
	}
	if strings.IndexByte(replacement, 0) >= 0 {
		return fmt.Errorf("replacement must not contain NUL")
	}
	if len(replacement) > maxRedactionReplacementBytes {
		return fmt.Errorf("replacement exceeds %d bytes", maxRedactionReplacementBytes)
	}
	if len(r.values) > maxRedactionValues {
		return fmt.Errorf("values exceed maximum count %d", maxRedactionValues)
	}
	totalBytes := 0
	for index, value := range r.values {
		if value == "" {
			return fmt.Errorf("value %d must not be empty", index)
		}
		if !utf8.ValidString(value) {
			return fmt.Errorf("value %d must be valid UTF-8", index)
		}
		if strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("value %d must not contain NUL", index)
		}
		if len(value) > maxRedactionValueBytes {
			return fmt.Errorf("value %d exceeds %d bytes", index, maxRedactionValueBytes)
		}
		if strings.Contains(replacement, value) {
			return fmt.Errorf("replacement must not contain redaction value %d", index)
		}
		totalBytes += len(value)
		if totalBytes > maxRedactionValuesTotalBytes {
			return fmt.Errorf("values exceed %d total bytes", maxRedactionValuesTotalBytes)
		}
	}
	return nil
}

func IsSensitiveEnvName(name string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range []string{
		"PASSWORD",
		"PASS",
		"SECRET",
		"TOKEN",
		"PRIVATE",
		"CREDENTIAL",
		"KEY",
		"PGPASS",
		"CONNECTION_STRING",
		"CONNECTIONSTRING",
		"CONNINFO",
		"DATABASE_URL",
	} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	if upper == "DSN" ||
		strings.HasSuffix(upper, "_DSN") ||
		strings.HasSuffix(upper, "_URL") ||
		strings.HasSuffix(upper, "_URI") {
		return true
	}
	return false
}

func RedactError(err error, values ...string) error {
	if err == nil {
		return nil
	}
	return redactedError{
		message: NewRedactor(values...).RedactString(err.Error()),
	}
}

func compileRedactor(values []string, replacement string) Redactor {
	if replacement == "" {
		replacement = defaultReplacement
	}
	compacted, err := compactSecrets(values)
	if err != nil {
		return invalidRedactor(replacement, err)
	}
	return Redactor{
		replacementValue: replacement,
		values:           compacted,
		compiled:         buildReplacer(compacted, replacement),
	}
}

func invalidRedactor(replacement string, err error) Redactor {
	if replacement == "" {
		replacement = defaultReplacement
	}
	return Redactor{
		replacementValue: replacement,
		validationErr:    err,
	}
}

func validateCompiledRedactor(redactor Redactor) Redactor {
	if err := redactor.validate(); err != nil {
		redactor.compiled = nil
		redactor.validationErr = err
	}
	return redactor
}

func buildReplacer(values []string, replacement string) *strings.Replacer {
	pairs := make([]string, 0, len(values)*2)
	for _, secret := range values {
		pairs = append(pairs, secret, replacement)
	}
	return strings.NewReplacer(pairs...)
}

func compactSecrets(values []string) ([]string, error) {
	if len(values) > maxRedactionValues {
		return nil, fmt.Errorf("values exceed maximum count %d", maxRedactionValues)
	}
	totalBytes := 0
	for index, value := range values {
		if value == "" {
			continue
		}
		if len(value) > maxRedactionValueBytes {
			return nil, fmt.Errorf("value %d exceeds %d bytes", index, maxRedactionValueBytes)
		}
		totalBytes += len(value)
		if totalBytes > maxRedactionValuesTotalBytes {
			return nil, fmt.Errorf("values exceed %d total bytes", maxRedactionValuesTotalBytes)
		}
	}

	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if len(result[i]) == len(result[j]) {
			return result[i] < result[j]
		}
		return len(result[i]) > len(result[j])
	})
	return result, nil
}
