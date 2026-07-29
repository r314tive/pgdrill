package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/durablefs"
	"github.com/r314tive/pgdrill/internal/model"
)

type blobState struct {
	ID             string
	Path           string
	SizeBytes      int64
	LastObservedAt time.Time
	Claims         []blobClaim
	ClaimDigest    string
}

func (b blobState) legacy() bool {
	return len(b.Claims) == 0
}

func (b blobState) hasRetention(class model.ArtifactRetentionClass) bool {
	for _, claim := range b.Claims {
		if claim.RetentionClass == class {
			return true
		}
	}
	return false
}

type temporaryState struct {
	RelativePath string
	Path         string
	SizeBytes    int64
	ModifiedAt   time.Time
}

type storeInventory struct {
	Managed          bool
	Blobs            []blobState
	BlobByID         map[string]blobState
	OrphanClaimIDs   []string
	TemporaryFiles   []temporaryState
	ReferenceIndex   referenceIndex
	StoreMetadata    StoreMetadata
	ActiveGCDigests  []string
	TrashGCDigests   []string
	PendingGCDigests []string
}

type referenceAggregate struct {
	ID               string                         `json:"id"`
	URI              string                         `json:"uri"`
	SizeBytes        int64                          `json:"size_bytes"`
	MediaTypes       []string                       `json:"media_types"`
	RetentionClasses []model.ArtifactRetentionClass `json:"retention_classes"`
	RedactionStates  []model.ArtifactRedactionState `json:"redaction_states"`
}

type referenceIndex struct {
	Occurrences int
	Foreign     int
	ByID        map[string]referenceAggregate
	Ordered     []referenceAggregate
	Digest      string
}

func scanStore(
	ctx context.Context,
	settings directorySettings,
	references []model.ArtifactRef,
) (storeInventory, error) {
	metadata, managed, err := readStoreMetadata(settings)
	if err != nil {
		return storeInventory{}, err
	}
	index, err := indexReferences(settings, references)
	if err != nil {
		return storeInventory{}, err
	}
	inventory := storeInventory{
		Managed:        managed,
		StoreMetadata:  metadata,
		BlobByID:       make(map[string]blobState),
		ReferenceIndex: index,
	}
	if err := inspectStoreRoot(settings.root, &inventory); err != nil {
		return storeInventory{}, err
	}
	blobs, err := scanBlobs(ctx, settings.root)
	if err != nil {
		return storeInventory{}, err
	}
	claims, orphanIDs, claimTemps, err := scanClaims(settings.root, blobs)
	if err != nil {
		return storeInventory{}, err
	}
	if len(inventory.TemporaryFiles)+len(claimTemps) > MaxTemporaryFiles {
		return storeInventory{}, fmt.Errorf(
			"artifact store exceeds maximum temporary file count %d",
			MaxTemporaryFiles,
		)
	}
	inventory.TemporaryFiles = append(inventory.TemporaryFiles, claimTemps...)
	for index := range blobs {
		blobs[index].Claims = claims[blobs[index].ID]
		blobs[index].ClaimDigest, err = digestClaims(blobs[index].Claims)
		if err != nil {
			return storeInventory{}, err
		}
		inventory.BlobByID[blobs[index].ID] = blobs[index]
	}
	inventory.Blobs = blobs
	inventory.OrphanClaimIDs = orphanIDs
	sort.Slice(inventory.TemporaryFiles, func(i, j int) bool {
		return inventory.TemporaryFiles[i].RelativePath < inventory.TemporaryFiles[j].RelativePath
	})
	state, err := inspectGCState(settings.root)
	if err != nil {
		return storeInventory{}, err
	}
	inventory.ActiveGCDigests = state.operations
	inventory.TrashGCDigests = state.trash
	inventory.PendingGCDigests = state.pending
	return inventory, nil
}

func inspectStoreRoot(root string, inventory *storeInventory) error {
	entries, err := durablefs.ReadDirBounded(root, MaxTemporaryFiles+5)
	if err != nil {
		return fmt.Errorf("read artifact store: %w", err)
	}
	allowedDirectories := map[string]struct{}{
		blobDirectoryName:  {},
		claimDirectoryName: {},
		gcDirectoryName:    {},
	}
	allowedFiles := map[string]struct{}{
		storeMetadataFileName: {},
		storeLockFileName:     {},
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if _, ok := allowedDirectories[entry.Name()]; ok {
			if err := requireRealDirectory(path); err != nil {
				return err
			}
			continue
		}
		if _, ok := allowedFiles[entry.Name()]; ok {
			if err := requirePrivateRegularFile(path, maxStoreJSONBytes); err != nil {
				return err
			}
			continue
		}
		if isArtifactTemporary(entry.Name()) {
			if len(inventory.TemporaryFiles) >= MaxTemporaryFiles {
				return fmt.Errorf(
					"artifact store exceeds maximum temporary file count %d",
					MaxTemporaryFiles,
				)
			}
			temp, err := inspectTemporary(root, path)
			if err != nil {
				return err
			}
			inventory.TemporaryFiles = append(inventory.TemporaryFiles, temp)
			continue
		}
		return fmt.Errorf("artifact store contains unexpected entry %q", entry.Name())
	}
	return nil
}

func scanBlobs(ctx context.Context, root string) ([]blobState, error) {
	digestRoot := filepath.Join(root, blobDirectoryName)
	if err := requireRealDirectory(digestRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []blobState{}, nil
		}
		return nil, fmt.Errorf("inspect artifact digest directory: %w", err)
	}
	prefixes, err := durablefs.ReadDirBounded(digestRoot, 256)
	if err != nil {
		return nil, fmt.Errorf("read artifact digest directory: %w", err)
	}
	blobs := make([]blobState, 0)
	for _, prefix := range prefixes {
		if !prefix.IsDir() || !isLowerHex(prefix.Name(), 2) {
			return nil, fmt.Errorf("artifact digest directory contains unexpected entry %q", prefix.Name())
		}
		prefixPath := filepath.Join(digestRoot, prefix.Name())
		if err := requireRealDirectory(prefixPath); err != nil {
			return nil, err
		}
		entries, err := durablefs.ReadDirBounded(prefixPath, MaxStoreBlobs)
		if err != nil {
			return nil, fmt.Errorf("read artifact prefix directory: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !isLowerHex(entry.Name(), 64) ||
				!strings.HasPrefix(entry.Name(), prefix.Name()) {
				return nil, fmt.Errorf(
					"artifact prefix %q contains unexpected entry %q",
					prefix.Name(),
					entry.Name(),
				)
			}
			if len(blobs) >= MaxStoreBlobs {
				return nil, fmt.Errorf("artifact store exceeds maximum blob count %d", MaxStoreBlobs)
			}
			path := filepath.Join(prefixPath, entry.Name())
			state, err := inspectBlob(ctx, path, entry.Name())
			if err != nil {
				return nil, err
			}
			blobs = append(blobs, state)
		}
	}
	sort.Slice(blobs, func(i, j int) bool { return blobs[i].ID < blobs[j].ID })
	return blobs, nil
}

func countStoredBlobs(digestRoot string) (int, error) {
	if err := requireRealDirectory(digestRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	prefixes, err := durablefs.ReadDirBounded(digestRoot, 256)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, prefix := range prefixes {
		if !prefix.IsDir() || !isLowerHex(prefix.Name(), 2) {
			return 0, fmt.Errorf(
				"artifact digest directory contains unexpected entry %q",
				prefix.Name(),
			)
		}
		prefixPath := filepath.Join(digestRoot, prefix.Name())
		if err := requireRealDirectory(prefixPath); err != nil {
			return 0, err
		}
		entries, err := durablefs.ReadDirBounded(prefixPath, MaxStoreBlobs)
		if err != nil {
			return 0, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !isLowerHex(entry.Name(), 64) ||
				!strings.HasPrefix(entry.Name(), prefix.Name()) {
				return 0, fmt.Errorf(
					"artifact prefix %q contains unexpected entry %q",
					prefix.Name(),
					entry.Name(),
				)
			}
			if err := requirePrivateRegularFile(
				filepath.Join(prefixPath, entry.Name()),
				int(model.MaxArtifactBytes),
			); err != nil {
				return 0, err
			}
			total++
			if total > MaxStoreBlobs {
				return total, nil
			}
		}
	}
	return total, nil
}

func inspectBlob(ctx context.Context, path, hexDigest string) (blobState, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return blobState{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return blobState{}, fmt.Errorf("%s is not a regular non-symbolic-link file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return blobState{}, fmt.Errorf("%s permissions %o are not private", path, info.Mode().Perm())
	}
	if info.Size() > model.MaxArtifactBytes {
		return blobState{}, fmt.Errorf("%s exceeds %d bytes", path, model.MaxArtifactBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return blobState{}, err
	}
	hasher := sha256.New()
	_, copyErr := io.CopyBuffer(
		hasher,
		&contextReader{ctx: ctx, reader: file},
		make([]byte, 32<<10),
	)
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return blobState{}, fmt.Errorf("hash artifact %s: %w", hexDigest, err)
	}
	if hex.EncodeToString(hasher.Sum(nil)) != hexDigest {
		return blobState{}, fmt.Errorf("artifact sha256:%s content digest does not match its path", hexDigest)
	}
	return blobState{
		ID:             "sha256:" + hexDigest,
		Path:           path,
		SizeBytes:      info.Size(),
		LastObservedAt: info.ModTime().UTC().Round(0),
	}, nil
}

func scanClaims(
	root string,
	blobs []blobState,
) (map[string][]blobClaim, []string, []temporaryState, error) {
	result := make(map[string][]blobClaim)
	blobByID := make(map[string]blobState, len(blobs))
	for _, blob := range blobs {
		blobByID[blob.ID] = blob
	}
	claimRoot := filepath.Join(root, claimDirectoryName)
	if err := requireRealDirectory(claimRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, []string{}, []temporaryState{}, nil
		}
		return nil, nil, nil, fmt.Errorf("inspect artifact claim directory: %w", err)
	}
	claimRootEntries, err := durablefs.ReadDirBounded(claimRoot, 1)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, entry := range claimRootEntries {
		if entry.Name() != blobDirectoryName || !entry.IsDir() {
			return nil, nil, nil, fmt.Errorf(
				"artifact claim directory contains unexpected entry %q",
				entry.Name(),
			)
		}
	}
	claimDigestRoot := filepath.Join(claimRoot, blobDirectoryName)
	if err := requireRealDirectory(claimDigestRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, []string{}, []temporaryState{}, nil
		}
		return nil, nil, nil, fmt.Errorf("inspect artifact claim directory: %w", err)
	}
	prefixes, err := durablefs.ReadDirBounded(claimDigestRoot, 256)
	if err != nil {
		return nil, nil, nil, err
	}
	orphanSet := make(map[string]struct{})
	temporary := []temporaryState{}
	digestCount := 0
	for _, prefix := range prefixes {
		if !prefix.IsDir() || !isLowerHex(prefix.Name(), 2) {
			return nil, nil, nil, fmt.Errorf(
				"artifact claim directory contains unexpected entry %q",
				prefix.Name(),
			)
		}
		prefixPath := filepath.Join(claimDigestRoot, prefix.Name())
		if err := requireRealDirectory(prefixPath); err != nil {
			return nil, nil, nil, err
		}
		digests, err := durablefs.ReadDirBounded(prefixPath, MaxStoreBlobs)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, digest := range digests {
			digestCount++
			if digestCount > MaxStoreBlobs {
				return nil, nil, nil, fmt.Errorf(
					"artifact store exceeds maximum claim digest count %d",
					MaxStoreBlobs,
				)
			}
			if !digest.IsDir() || !isLowerHex(digest.Name(), 64) ||
				!strings.HasPrefix(digest.Name(), prefix.Name()) {
				return nil, nil, nil, fmt.Errorf(
					"artifact claim prefix %q contains unexpected entry %q",
					prefix.Name(),
					digest.Name(),
				)
			}
			id := "sha256:" + digest.Name()
			claimPath := filepath.Join(prefixPath, digest.Name())
			if err := requireRealDirectory(claimPath); err != nil {
				return nil, nil, nil, err
			}
			entries, err := durablefs.ReadDirBounded(claimPath, MaxClaimsPerBlob)
			if err != nil {
				return nil, nil, nil, err
			}
			for _, entry := range entries {
				path := filepath.Join(claimPath, entry.Name())
				if isArtifactTemporary(entry.Name()) {
					if len(temporary) >= MaxTemporaryFiles {
						return nil, nil, nil, fmt.Errorf(
							"artifact store exceeds maximum temporary file count %d",
							MaxTemporaryFiles,
						)
					}
					temp, err := inspectTemporary(root, path)
					if err != nil {
						return nil, nil, nil, err
					}
					temporary = append(temporary, temp)
					continue
				}
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") ||
					!isLowerHex(strings.TrimSuffix(entry.Name(), ".json"), 64) {
					return nil, nil, nil, fmt.Errorf(
						"artifact claims for %s contain unexpected entry %q",
						id,
						entry.Name(),
					)
				}
				claim, err := readJSONFile[blobClaim](path, maxClaimJSONBytes)
				if err != nil {
					return nil, nil, nil, fmt.Errorf("read artifact blob claim: %w", err)
				}
				if err := claim.validate(); err != nil {
					return nil, nil, nil, err
				}
				claimDigest, _, err := claim.digest()
				if err != nil {
					return nil, nil, nil, err
				}
				if strings.TrimPrefix(claimDigest, "sha256:")+".json" != entry.Name() {
					return nil, nil, nil, fmt.Errorf("artifact blob claim filename does not match content")
				}
				if claim.ArtifactID != id {
					return nil, nil, nil, fmt.Errorf("artifact blob claim identity does not match directory")
				}
				if blob, exists := blobByID[id]; exists && claim.SizeBytes != blob.SizeBytes {
					return nil, nil, nil, fmt.Errorf("artifact blob claim size does not match content")
				}
				result[id] = append(result[id], claim)
			}
			if _, exists := blobByID[id]; !exists {
				orphanSet[id] = struct{}{}
			}
		}
	}
	for id := range result {
		sort.Slice(result[id], func(i, j int) bool {
			left, _, _ := result[id][i].digest()
			right, _, _ := result[id][j].digest()
			return left < right
		})
	}
	orphanIDs := make([]string, 0, len(orphanSet))
	for id := range orphanSet {
		orphanIDs = append(orphanIDs, id)
	}
	sort.Strings(orphanIDs)
	return result, orphanIDs, temporary, nil
}

func digestClaims(claims []blobClaim) (string, error) {
	digests := make([]string, 0, len(claims))
	for _, claim := range claims {
		digest, _, err := claim.digest()
		if err != nil {
			return "", err
		}
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	payload, err := marshalBoundedJSON(digests, maxClaimJSONBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func indexReferences(
	settings directorySettings,
	references []model.ArtifactRef,
) (referenceIndex, error) {
	if len(references) > MaxGCReferences {
		return referenceIndex{}, fmt.Errorf(
			"artifact references exceed maximum count %d",
			MaxGCReferences,
		)
	}
	index := referenceIndex{ByID: make(map[string]referenceAggregate)}
	for position, ref := range references {
		if err := ref.Validate(); err != nil {
			return referenceIndex{}, fmt.Errorf(
				"validate artifact reference %d: %w",
				position,
				err,
			)
		}
		hexDigest := strings.TrimPrefix(ref.ID, "sha256:")
		if ref.URI != settings.uri(hexDigest) {
			index.Foreign++
			continue
		}
		index.Occurrences++
		aggregate, exists := index.ByID[ref.ID]
		if !exists {
			aggregate = referenceAggregate{
				ID:        ref.ID,
				URI:       ref.URI,
				SizeBytes: ref.SizeBytes,
			}
		} else if aggregate.URI != ref.URI || aggregate.SizeBytes != ref.SizeBytes {
			return referenceIndex{}, fmt.Errorf(
				"artifact reference %s has conflicting immutable identity",
				ref.ID,
			)
		}
		aggregate.MediaTypes = appendUniqueSortedString(aggregate.MediaTypes, ref.MediaType)
		aggregate.RetentionClasses = appendUniqueSortedRetention(
			aggregate.RetentionClasses,
			ref.RetentionClass,
		)
		aggregate.RedactionStates = appendUniqueSortedRedaction(
			aggregate.RedactionStates,
			ref.RedactionState,
		)
		index.ByID[ref.ID] = aggregate
	}
	index.Ordered = make([]referenceAggregate, 0, len(index.ByID))
	for _, aggregate := range index.ByID {
		index.Ordered = append(index.Ordered, aggregate)
	}
	sort.Slice(index.Ordered, func(i, j int) bool {
		return index.Ordered[i].ID < index.Ordered[j].ID
	})
	payload, err := marshalBoundedJSON(index.Ordered, maxGCPlanJSONBytes)
	if err != nil {
		return referenceIndex{}, err
	}
	sum := sha256.Sum256(payload)
	index.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return index, nil
}

func appendUniqueSortedString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	values = append(values, value)
	sort.Strings(values)
	return values
}

func appendUniqueSortedRetention(
	values []model.ArtifactRetentionClass,
	value model.ArtifactRetentionClass,
) []model.ArtifactRetentionClass {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	values = append(values, value)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}

func appendUniqueSortedRedaction(
	values []model.ArtifactRedactionState,
	value model.ArtifactRedactionState,
) []model.ArtifactRedactionState {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	values = append(values, value)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}

func inspectTemporary(root, path string) (temporaryState, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return temporaryState{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return temporaryState{}, fmt.Errorf("%s is not a regular non-symbolic-link file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return temporaryState{}, fmt.Errorf("%s permissions %o are not private", path, info.Mode().Perm())
	}
	if info.Size() > model.MaxArtifactBytes {
		return temporaryState{}, fmt.Errorf("%s exceeds %d bytes", path, model.MaxArtifactBytes)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return temporaryState{}, fmt.Errorf("artifact temporary path escapes store")
	}
	return temporaryState{
		RelativePath: filepath.ToSlash(relative),
		Path:         path,
		SizeBytes:    info.Size(),
		ModifiedAt:   info.ModTime().UTC().Round(0),
	}, nil
}

func requirePrivateRegularFile(path string, maxBytes int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular non-symbolic-link file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s permissions %o are not private", path, info.Mode().Perm())
	}
	if info.Size() > int64(maxBytes) {
		return fmt.Errorf("%s exceeds %d bytes", path, maxBytes)
	}
	return nil
}

func isArtifactTemporary(name string) bool {
	return strings.HasPrefix(name, ".artifact-") && strings.HasSuffix(name, ".tmp")
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
