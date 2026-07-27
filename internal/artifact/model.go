package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/r314tive/pgdrill/internal/model"
)

const (
	CurrentStoreSchemaVersion        = "pgdrill.artifact-store/v1alpha1"
	CurrentBlobClaimSchemaVersion    = "pgdrill.artifact-blob-claim/v1alpha1"
	CurrentVerificationSchemaVersion = "pgdrill.artifact-verification/v1alpha1"
	CurrentGCPlanSchemaVersion       = "pgdrill.artifact-gc-plan/v1alpha1"
	CurrentGCResultSchemaVersion     = "pgdrill.artifact-gc-result/v1alpha1"

	CurrentLayoutVersion = 1
	MaxGCReferences      = 1_000_000
	MaxStoreBlobs        = 1_000_000
	MaxClaimsPerBlob     = 1_024
	MaxTemporaryFiles    = 100_000
	maxStoreJSONBytes    = 64 << 10
	maxClaimJSONBytes    = 64 << 10
	maxGCPlanJSONBytes   = 64 << 20
)

const (
	storeMetadataFileName = "store.json"
	storeLockFileName     = ".lock"
	blobDirectoryName     = "sha256"
	claimDirectoryName    = "claims"
	gcDirectoryName       = "gc"
)

// StoreMetadata binds the on-disk layout to the URI prefix emitted in durable
// report references. It is immutable after the first successful write.
type StoreMetadata struct {
	SchemaVersion string `json:"schema_version"`
	LayoutVersion int    `json:"layout_version"`
	URIBase       string `json:"uri_base"`
}

func (m StoreMetadata) validate(expectedURIBase string) error {
	if m.SchemaVersion != CurrentStoreSchemaVersion {
		return fmt.Errorf(
			"artifact store schema_version %q is unsupported; expected %q",
			m.SchemaVersion,
			CurrentStoreSchemaVersion,
		)
	}
	if m.LayoutVersion != CurrentLayoutVersion {
		return fmt.Errorf(
			"artifact store layout_version %d is unsupported; expected %d",
			m.LayoutVersion,
			CurrentLayoutVersion,
		)
	}
	if m.URIBase != expectedURIBase {
		return fmt.Errorf(
			"artifact store uri_base %q does not match configured value %q",
			m.URIBase,
			expectedURIBase,
		)
	}
	return nil
}

// blobClaim is an immutable observation that a producer classified one exact
// content digest before publishing a durable reference. Multiple claims may
// exist for the same digest; GC uses the strongest observed retention class.
type blobClaim struct {
	SchemaVersion  string                       `json:"schema_version"`
	ArtifactID     string                       `json:"artifact_id"`
	SizeBytes      int64                        `json:"size_bytes"`
	MediaType      string                       `json:"media_type"`
	RetentionClass model.ArtifactRetentionClass `json:"retention_class"`
	RedactionState model.ArtifactRedactionState `json:"redaction_state"`
}

func newBlobClaim(ref model.ArtifactRef) blobClaim {
	return blobClaim{
		SchemaVersion:  CurrentBlobClaimSchemaVersion,
		ArtifactID:     ref.ID,
		SizeBytes:      ref.SizeBytes,
		MediaType:      ref.MediaType,
		RetentionClass: ref.RetentionClass,
		RedactionState: ref.RedactionState,
	}
}

func (c blobClaim) validate() error {
	if c.SchemaVersion != CurrentBlobClaimSchemaVersion {
		return fmt.Errorf(
			"artifact blob claim schema_version must be %q",
			CurrentBlobClaimSchemaVersion,
		)
	}
	ref := model.ArtifactRef{
		SchemaVersion:  model.CurrentArtifactReferenceSchemaVersion,
		ID:             c.ArtifactID,
		URI:            "claim/sha256/" + strings.TrimPrefix(c.ArtifactID, "sha256:"),
		SizeBytes:      c.SizeBytes,
		MediaType:      c.MediaType,
		RetentionClass: c.RetentionClass,
		RedactionState: c.RedactionState,
	}
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("validate artifact blob claim: %w", err)
	}
	return nil
}

func (c blobClaim) digest() (string, []byte, error) {
	if err := c.validate(); err != nil {
		return "", nil, err
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", nil, fmt.Errorf("encode artifact blob claim: %w", err)
	}
	payload = append(payload, '\n')
	if len(payload) > maxClaimJSONBytes {
		return "", nil, fmt.Errorf("artifact blob claim exceeds %d bytes", maxClaimJSONBytes)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), payload, nil
}
