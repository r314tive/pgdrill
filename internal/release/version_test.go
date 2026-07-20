package release

import "testing"

func TestValidateVersion(t *testing.T) {
	for _, version := range []string{"v0.1.0", "v0.1.0-alpha.6", "v1.2.3-rc.1+build.7"} {
		t.Run(version, func(t *testing.T) {
			if err := ValidateVersion(version); err != nil {
				t.Fatalf("validate version: %v", err)
			}
		})
	}
}

func TestValidateVersionRejectsInvalidSemver(t *testing.T) {
	for _, version := range []string{"0.1.0", "v0.1", "v01.2.3", "v1.2.3-01", "v1.2.3+", "v1.2.3-alpha_1"} {
		t.Run(version, func(t *testing.T) {
			if err := ValidateVersion(version); err == nil {
				t.Fatalf("expected %q to fail", version)
			}
		})
	}
}
