package command

import "strings"

const defaultReplacement = "[REDACTED]"

type Redactor struct {
	Replacement string
	Values      []string
}

func NewRedactor(values ...string) Redactor {
	return Redactor{
		Replacement: defaultReplacement,
		Values:      compactSecrets(values),
	}
}

func (r Redactor) WithValues(values ...string) Redactor {
	r.Values = append(append([]string{}, r.Values...), compactSecrets(values)...)
	if r.Replacement == "" {
		r.Replacement = defaultReplacement
	}
	return r
}

func (r Redactor) RedactString(value string) string {
	replacement := r.Replacement
	if replacement == "" {
		replacement = defaultReplacement
	}
	for _, secret := range r.Values {
		value = strings.ReplaceAll(value, secret, replacement)
	}
	return value
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
	} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

func compactSecrets(values []string) []string {
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
	return result
}
