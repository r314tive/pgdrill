package model

import (
	"strings"
	"testing"
)

func TestArtifactReferenceValidation(t *testing.T) {
	metadata, err := NewArtifactMetadata("application/yaml; charset=utf-8", ArtifactRetentionHistory, ArtifactRedactionNotRequired)
	if err != nil {
		t.Fatalf("NewArtifactMetadata() error = %v", err)
	}
	ref, err := NewArtifactRef(
		"sha256:"+strings.Repeat("a", 64),
		"report.json.artifacts/sha256/aa/"+strings.Repeat("a", 64),
		1024,
		metadata,
	)
	if err != nil {
		t.Fatalf("NewArtifactRef() error = %v", err)
	}
	if ref.MediaType != "application/yaml; charset=utf-8" {
		t.Fatalf("unexpected canonical media type %q", ref.MediaType)
	}
}

func TestArtifactReferenceRejectsUnsafeOrUnclassifiedValues(t *testing.T) {
	valid := ArtifactRef{
		SchemaVersion:  CurrentArtifactReferenceSchemaVersion,
		ID:             "sha256:" + strings.Repeat("a", 64),
		URI:            "report.json.artifacts/sha256/aa/blob",
		SizeBytes:      1,
		MediaType:      "text/plain",
		RetentionClass: ArtifactRetentionHistory,
		RedactionState: ArtifactRedactionRedacted,
	}
	tests := []struct {
		name string
		edit func(*ArtifactRef)
		want string
	}{
		{name: "digest", edit: func(ref *ArtifactRef) { ref.ID = "md5:no" }, want: "sha256"},
		{name: "parent uri", edit: func(ref *ArtifactRef) { ref.URI = "../secret" }, want: "descendant"},
		{name: "query uri", edit: func(ref *ArtifactRef) { ref.URI = "s3://bucket/key?token=secret" }, want: "query"},
		{name: "size", edit: func(ref *ArtifactRef) { ref.SizeBytes = MaxArtifactBytes + 1 }, want: "size_bytes"},
		{name: "retention", edit: func(ref *ArtifactRef) { ref.RetentionClass = "forever" }, want: "retention_class"},
		{name: "redaction", edit: func(ref *ArtifactRef) { ref.RedactionState = "unredacted" }, want: "redaction_state"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref := valid
			tt.edit(&ref)
			if err := ref.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.want)
			}
		})
	}
}
