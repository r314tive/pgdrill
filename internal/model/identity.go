package model

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxIdentityBytes = 512

// ValidateIdentity applies the shared durable run and attempt identity
// contract used by events, reports, checkpoints, and local history.
func ValidateIdentity(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not contain surrounding whitespace", field)
	}
	if len(value) > maxIdentityBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, maxIdentityBytes)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s must not contain control characters", field)
	}
	return nil
}
