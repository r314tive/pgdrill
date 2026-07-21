package model

import (
	"fmt"
	"mime"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	CurrentArtifactReferenceSchemaVersion = "pgdrill.artifact-reference/v1alpha1"
	MaxArtifactBytes                      = int64(64 << 20)
	maxArtifactURIBytes                   = 2048
	maxArtifactMediaTypeBytes             = 255
)

type ArtifactRetentionClass string

const (
	ArtifactRetentionRun     ArtifactRetentionClass = "run"
	ArtifactRetentionHistory ArtifactRetentionClass = "history"
	ArtifactRetentionAudit   ArtifactRetentionClass = "audit"
)

func (c ArtifactRetentionClass) IsKnown() bool {
	switch c {
	case ArtifactRetentionRun, ArtifactRetentionHistory, ArtifactRetentionAudit:
		return true
	default:
		return false
	}
}

type ArtifactRedactionState string

const (
	ArtifactRedactionRedacted    ArtifactRedactionState = "redacted"
	ArtifactRedactionNotRequired ArtifactRedactionState = "not_required"
)

func (s ArtifactRedactionState) IsKnown() bool {
	return s == ArtifactRedactionRedacted || s == ArtifactRedactionNotRequired
}

// ArtifactMetadata classifies a blob before it crosses the durable artifact
// boundary. There is deliberately no unredacted state: callers must either
// redact content first or prove that the artifact schema cannot contain a
// secret.
type ArtifactMetadata struct {
	MediaType      string                 `json:"media_type"`
	RetentionClass ArtifactRetentionClass `json:"retention_class"`
	RedactionState ArtifactRedactionState `json:"redaction_state"`
}

func NewArtifactMetadata(mediaType string, retention ArtifactRetentionClass, redaction ArtifactRedactionState) (ArtifactMetadata, error) {
	canonicalMediaType, err := canonicalArtifactMediaType(mediaType)
	if err != nil {
		return ArtifactMetadata{}, err
	}
	metadata := ArtifactMetadata{
		MediaType:      canonicalMediaType,
		RetentionClass: retention,
		RedactionState: redaction,
	}
	if err := metadata.Validate(); err != nil {
		return ArtifactMetadata{}, err
	}
	return metadata, nil
}

func (m ArtifactMetadata) Validate() error {
	canonicalMediaType, err := canonicalArtifactMediaType(m.MediaType)
	if err != nil {
		return err
	}
	if m.MediaType != canonicalMediaType {
		return fmt.Errorf("media_type must use canonical value %q", canonicalMediaType)
	}
	if !m.RetentionClass.IsKnown() {
		return fmt.Errorf("retention_class %q is unsupported", m.RetentionClass)
	}
	if !m.RedactionState.IsKnown() {
		return fmt.Errorf("redaction_state %q is unsupported", m.RedactionState)
	}
	return nil
}

type ArtifactRef struct {
	SchemaVersion  string                 `json:"schema_version"`
	ID             string                 `json:"id"`
	URI            string                 `json:"uri"`
	SizeBytes      int64                  `json:"size_bytes"`
	MediaType      string                 `json:"media_type"`
	RetentionClass ArtifactRetentionClass `json:"retention_class"`
	RedactionState ArtifactRedactionState `json:"redaction_state"`
}

func NewArtifactRef(id, uri string, sizeBytes int64, metadata ArtifactMetadata) (ArtifactRef, error) {
	ref := ArtifactRef{
		SchemaVersion:  CurrentArtifactReferenceSchemaVersion,
		ID:             id,
		URI:            uri,
		SizeBytes:      sizeBytes,
		MediaType:      metadata.MediaType,
		RetentionClass: metadata.RetentionClass,
		RedactionState: metadata.RedactionState,
	}
	if err := ref.Validate(); err != nil {
		return ArtifactRef{}, err
	}
	return ref, nil
}

func (r ArtifactRef) Validate() error {
	if r.SchemaVersion != CurrentArtifactReferenceSchemaVersion {
		return fmt.Errorf("schema_version must be %q", CurrentArtifactReferenceSchemaVersion)
	}
	if !IsSHA256Digest(r.ID) || r.ID != strings.ToLower(r.ID) {
		return fmt.Errorf("id must be a canonical lowercase sha256 digest")
	}
	if err := validateArtifactURI(r.URI); err != nil {
		return err
	}
	if r.SizeBytes < 0 || r.SizeBytes > MaxArtifactBytes {
		return fmt.Errorf("size_bytes must be between 0 and %d", MaxArtifactBytes)
	}
	return (ArtifactMetadata{
		MediaType:      r.MediaType,
		RetentionClass: r.RetentionClass,
		RedactionState: r.RedactionState,
	}).Validate()
}

func canonicalArtifactMediaType(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("media_type is required")
	}
	if value != strings.TrimSpace(value) || len(value) > maxArtifactMediaTypeBytes || !utf8.ValidString(value) {
		return "", fmt.Errorf("media_type must be bounded canonical UTF-8")
	}
	mediaType, parameters, err := mime.ParseMediaType(value)
	if err != nil {
		return "", fmt.Errorf("parse media_type: %w", err)
	}
	return mime.FormatMediaType(strings.ToLower(mediaType), parameters), nil
}

func validateArtifactURI(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("uri is required")
	}
	if value != strings.TrimSpace(value) || len(value) > maxArtifactURIBytes || !utf8.ValidString(value) {
		return fmt.Errorf("uri must be bounded canonical UTF-8")
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("uri must not contain control characters")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("parse uri: %w", err)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("uri must not contain credentials, query parameters, or fragments")
	}
	if parsed.Scheme == "" {
		if parsed.Host != "" || parsed.Path == "" || strings.HasPrefix(parsed.Path, "/") || strings.Contains(parsed.Path, `\`) {
			return fmt.Errorf("relative uri must be a non-empty portable path")
		}
		if cleaned := path.Clean(parsed.Path); cleaned == "." || cleaned != parsed.Path || strings.HasPrefix(cleaned, "../") {
			return fmt.Errorf("relative uri must be a canonical descendant path")
		}
		return nil
	}
	if parsed.Scheme != strings.ToLower(parsed.Scheme) {
		return fmt.Errorf("uri scheme must be lowercase")
	}
	if parsed.Host == "" && parsed.Opaque == "" {
		return fmt.Errorf("absolute uri requires an authority or opaque value")
	}
	return nil
}
