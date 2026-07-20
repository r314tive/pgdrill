package release

import (
	"fmt"
	"strings"
)

func ValidateVersion(version string) error {
	if !strings.HasPrefix(version, "v") {
		return fmt.Errorf("release version %q must start with v", version)
	}
	value := strings.TrimPrefix(version, "v")
	coreAndPre, build, hasBuild := strings.Cut(value, "+")
	if hasBuild {
		if err := validateIdentifiers(build, false); err != nil {
			return fmt.Errorf("invalid release build metadata: %w", err)
		}
	}
	core, prerelease, hasPrerelease := strings.Cut(coreAndPre, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return fmt.Errorf("release version %q must contain major.minor.patch", version)
	}
	for _, part := range parts {
		if !isDecimal(part) || (len(part) > 1 && part[0] == '0') {
			return fmt.Errorf("release version %q has an invalid numeric component", version)
		}
	}
	if hasPrerelease {
		if err := validateIdentifiers(prerelease, true); err != nil {
			return fmt.Errorf("invalid release prerelease: %w", err)
		}
	}
	return nil
}

func validateIdentifiers(value string, rejectNumericLeadingZero bool) error {
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return fmt.Errorf("empty identifier")
		}
		for _, char := range identifier {
			if !isASCIILetter(char) && (char < '0' || char > '9') && char != '-' {
				return fmt.Errorf("identifier %q contains unsupported characters", identifier)
			}
		}
		if rejectNumericLeadingZero && len(identifier) > 1 && identifier[0] == '0' && isDecimal(identifier) {
			return fmt.Errorf("numeric identifier %q has a leading zero", identifier)
		}
	}
	return nil
}

func isDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func isASCIILetter(char rune) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}
