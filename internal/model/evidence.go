package model

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxCheckMessageBytes         = 16 << 10
	MaxFailureMessageBytes       = 16 << 10
	MaxReportAttributes          = 128
	MaxReportAttributeKeyBytes   = 256
	MaxReportAttributeValueBytes = 16 << 10
)

func (c Check) Validate() error {
	if err := ValidateIdentity("check name", c.Name); err != nil {
		return err
	}
	if !c.Status.IsTerminal() {
		return fmt.Errorf("check %q has non-terminal status %q", c.Name, c.Status)
	}
	if c.Probe != "" && !c.Probe.IsKnown() {
		return fmt.Errorf("check %q has unsupported probe %q", c.Name, c.Probe)
	}
	if err := validateBoundedUTF8("check message", c.Message, MaxCheckMessageBytes); err != nil {
		return err
	}
	if err := validateStringAttributes("check", c.Attributes); err != nil {
		return err
	}
	return validateIdentityReferences(
		"check evidence_ids",
		c.EvidenceIDs,
		MaxEvidenceRecordsPerReport,
	)
}

func (f DrillFailure) Validate() error {
	if !f.Stage.IsKnown() {
		return fmt.Errorf("failure has unsupported stage %q", f.Stage)
	}
	if strings.TrimSpace(f.Message) == "" {
		return fmt.Errorf("failure message is required")
	}
	if err := validateBoundedUTF8("failure message", f.Message, MaxFailureMessageBytes); err != nil {
		return err
	}
	return validateIdentityReferences(
		"failure evidence_ids",
		f.EvidenceIDs,
		MaxEvidenceRecordsPerReport,
	)
}

func (r EvidenceRecord) Validate() error {
	if err := ValidateIdentity("evidence id", r.ID); err != nil {
		return err
	}
	if !r.Kind.IsKnown() {
		return fmt.Errorf("unsupported evidence kind %q", r.Kind)
	}
	if err := ValidateIdentity("evidence source", r.Source); err != nil {
		return err
	}
	if r.CollectedAt.IsZero() {
		return fmt.Errorf("evidence collected_at is required")
	}
	if r.Kind == EvidenceCommand {
		if r.Command == nil {
			return fmt.Errorf("command evidence payload is required")
		}
		if err := r.Command.Validate(); err != nil {
			return fmt.Errorf("invalid command payload: %w", err)
		}
	} else if r.Command != nil {
		return fmt.Errorf("evidence kind %q must not contain command payload", r.Kind)
	}
	if len(r.ArtifactIDs) > MaxArtifactIDsPerEvidence {
		return fmt.Errorf(
			"artifact_ids exceed maximum count %d",
			MaxArtifactIDsPerEvidence,
		)
	}
	seenArtifacts := make(map[string]struct{}, len(r.ArtifactIDs))
	for _, artifactID := range r.ArtifactIDs {
		if !IsSHA256Digest(artifactID) {
			return fmt.Errorf("artifact id %q must be a canonical sha256 digest", artifactID)
		}
		if _, duplicate := seenArtifacts[artifactID]; duplicate {
			return fmt.Errorf("duplicate artifact id %q", artifactID)
		}
		seenArtifacts[artifactID] = struct{}{}
	}
	return validateStringAttributes("evidence", r.Attributes)
}

func (c CommandEvidence) Validate() error {
	if strings.TrimSpace(c.Path) == "" {
		return fmt.Errorf("path is required")
	}
	for _, field := range []struct {
		name     string
		value    string
		maxBytes int
	}{
		{name: "path", value: c.Path, maxBytes: MaxCommandPathBytes},
		{name: "resolved_path", value: c.ResolvedPath, maxBytes: MaxCommandPathBytes},
		{name: "work_dir", value: c.WorkDir, maxBytes: MaxCommandPathBytes},
	} {
		if err := validateCommandText(field.name, field.value, field.maxBytes); err != nil {
			return err
		}
	}
	if c.StartedAt.IsZero() || c.FinishedAt.IsZero() {
		return fmt.Errorf("started_at and finished_at are required")
	}
	if c.FinishedAt.Before(c.StartedAt) {
		return fmt.Errorf("finished_at must not be earlier than started_at")
	}
	if c.DurationMillis < 0 {
		return fmt.Errorf("duration_millis must not be negative")
	}
	if expected := c.FinishedAt.Sub(c.StartedAt).Milliseconds(); c.DurationMillis != expected {
		return fmt.Errorf(
			"duration_millis %d does not match timestamp interval %d",
			c.DurationMillis,
			expected,
		)
	}
	if c.StdoutBytes < 0 || c.StderrBytes < 0 {
		return fmt.Errorf("captured byte counts must not be negative")
	}
	if len(c.Args) > MaxCommandArguments {
		return fmt.Errorf("args exceed maximum count %d", MaxCommandArguments)
	}
	if len(c.Env) > MaxCommandEnvironmentEntries {
		return fmt.Errorf("env exceeds maximum count %d", MaxCommandEnvironmentEntries)
	}
	for index, argument := range c.Args {
		if err := validateCommandText(
			fmt.Sprintf("arg %d", index),
			argument,
			MaxCommandArgumentBytes,
		); err != nil {
			return err
		}
	}
	for name, value := range c.Env {
		if err := validateCommandText("env name", name, MaxCommandEnvironmentNameBytes); err != nil {
			return err
		}
		if name == "" || strings.Contains(name, "=") {
			return fmt.Errorf("env name %q is invalid", name)
		}
		if err := validateCommandText(
			fmt.Sprintf("env %q value", name),
			value,
			MaxCommandEnvironmentValueBytes,
		); err != nil {
			return err
		}
	}
	for _, stream := range []struct {
		name  string
		value string
	}{
		{name: "stdout", value: c.Stdout},
		{name: "stderr", value: c.Stderr},
	} {
		if !utf8.ValidString(stream.value) {
			return fmt.Errorf("%s must be valid UTF-8", stream.name)
		}
		if len(stream.value) > MaxCommandEvidenceBytes {
			return fmt.Errorf(
				"%s exceeds maximum evidence size %d",
				stream.name,
				MaxCommandEvidenceBytes,
			)
		}
	}
	status := c.ExitStatus
	if err := validateBoundedUTF8("exit status error", status.Error, MaxCommandErrorBytes); err != nil {
		return err
	}
	if err := validateExitStatus(status); err != nil {
		return err
	}
	return nil
}

func validateExitStatus(status ExitStatus) error {
	if status.TimedOut && status.Canceled {
		return fmt.Errorf("exit status cannot be both timed_out and canceled")
	}
	if status.ExitCode < -1 {
		return fmt.Errorf("exit status exit_code must not be less than -1")
	}
	if status.Success &&
		(!status.Started ||
			!status.Exited ||
			status.ExitCode != 0 ||
			status.TimedOut ||
			status.Canceled ||
			status.Error != "") {
		return fmt.Errorf("successful exit status is internally inconsistent")
	}
	if status.Started != status.Exited {
		return fmt.Errorf("terminal exit status requires started and exited to match")
	}
	if !status.Started {
		if status.ExitCode != -1 {
			return fmt.Errorf("exit status that did not start requires exit_code -1")
		}
		if status.Error == "" && !status.TimedOut && !status.Canceled {
			return fmt.Errorf("exit status that did not start requires an error or context outcome")
		}
		return nil
	}
	if status.Success {
		return nil
	}
	if status.ExitCode == 0 &&
		!status.TimedOut &&
		!status.Canceled &&
		status.Error == "" {
		return fmt.Errorf("unsuccessful exit status with exit_code 0 requires an error")
	}
	return nil
}

func validateCommandText(field, value string, maxBytes int) error {
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s must not contain NUL", field)
	}
	return validateBoundedUTF8(field, value, maxBytes)
}

func validateBoundedUTF8(field, value string, maxBytes int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds maximum size %d", field, maxBytes)
	}
	return nil
}

func validateIdentityReferences(field string, values []string, maximum int) error {
	if len(values) > maximum {
		return fmt.Errorf("%s exceed maximum count %d", field, maximum)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := ValidateIdentity(field, value); err != nil {
			return err
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%s contain duplicate reference %q", field, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateStringAttributes(owner string, attributes map[string]string) error {
	if len(attributes) > MaxReportAttributes {
		return fmt.Errorf(
			"%s attributes exceed maximum count %d",
			owner,
			MaxReportAttributes,
		)
	}
	for key, value := range attributes {
		if strings.TrimSpace(key) == "" || key != strings.TrimSpace(key) {
			return fmt.Errorf("%s attribute key must be non-empty and canonical", owner)
		}
		if err := validateBoundedUTF8(
			owner+" attribute key",
			key,
			MaxReportAttributeKeyBytes,
		); err != nil {
			return err
		}
		if strings.IndexFunc(key, unicode.IsControl) >= 0 {
			return fmt.Errorf("%s attribute key must not contain control characters", owner)
		}
		if err := validateBoundedUTF8(
			fmt.Sprintf("%s attribute %q value", owner, key),
			value,
			MaxReportAttributeValueBytes,
		); err != nil {
			return err
		}
	}
	return nil
}
