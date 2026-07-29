package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/r314tive/pgdrill/internal/filelock"
	"github.com/r314tive/pgdrill/internal/model"
)

const (
	currentMigrationRecordSchema = "pgdrill.history-migration-record/v1"
	migrationRecordFileName      = "migration.json"
	migrationParentLockFileName  = ".pgdrill-history-migration.lock"
	historyStoreLockFileName     = ".lock"

	migrationStepAfterFileCopy   = "after_file_copy"
	migrationStepAfterStageCheck = "after_stage_verify"
	migrationStepAfterPublish    = "after_publish"
)

// MigrationPlan binds a copy-on-migrate operation to one exact source tree.
// The source remains untouched and is the rollback copy after publication.
type MigrationPlan struct {
	SchemaVersion            string `json:"schema_version"`
	CompatibilityFloor       string `json:"compatibility_floor"`
	Source                   string `json:"source"`
	Destination              string `json:"destination"`
	SourceStoreSchemaVersion string `json:"source_store_schema_version"`
	TargetStoreSchemaVersion string `json:"target_store_schema_version"`
	LayoutVersion            int    `json:"layout_version"`
	SourceSnapshotDigest     string `json:"source_snapshot_digest"`
	HistoricalPayloadDigest  string `json:"historical_payload_digest"`
	Files                    int    `json:"files"`
	Bytes                    int64  `json:"bytes"`
	Runs                     int    `json:"runs"`
	Attempts                 int    `json:"attempts"`
	TerminalReports          int    `json:"terminal_reports"`
	IncompleteAttempts       int    `json:"incomplete_attempts"`
	Events                   int    `json:"events"`
	Digest                   string `json:"digest"`
}

// MigrationResult describes a fully verified stable destination.
type MigrationResult struct {
	SchemaVersion        string             `json:"schema_version"`
	PlanDigest           string             `json:"plan_digest"`
	Source               string             `json:"source"`
	Destination          string             `json:"destination"`
	SourceSnapshotDigest string             `json:"source_snapshot_digest"`
	Files                int                `json:"files"`
	Bytes                int64              `json:"bytes"`
	AlreadyApplied       bool               `json:"already_applied"`
	Verification         VerificationResult `json:"verification"`
}

type migrationRecord struct {
	SchemaVersion            string `json:"schema_version"`
	CompatibilityFloor       string `json:"compatibility_floor"`
	PlanDigest               string `json:"plan_digest"`
	SourceStoreSchemaVersion string `json:"source_store_schema_version"`
	SourceSnapshotDigest     string `json:"source_snapshot_digest"`
	HistoricalPayloadDigest  string `json:"historical_payload_digest"`
}

type migrationSnapshot struct {
	Digest        string
	PayloadDigest string
	Files         int
	Bytes         int64
}

type migrationCounts struct {
	Runs               int
	Attempts           int
	TerminalReports    int
	IncompleteAttempts int
	Events             int
}

type migrationManifestEntry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size,omitempty"`
	Digest string `json:"digest,omitempty"`
}

type migrationDirectory struct {
	path string
	mode os.FileMode
}

// PlanMigration fully reads a legacy store and returns a deterministic plan.
// It never creates or modifies the destination.
func (s DirectoryStore) PlanMigration(
	ctx context.Context,
	destination string,
) (MigrationPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	source, destination, err := migrationPaths(s.Path, destination)
	if err != nil {
		return MigrationPlan{}, err
	}
	if _, err := os.Lstat(destination); err == nil {
		return MigrationPlan{}, fmt.Errorf("history migration destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return MigrationPlan{}, fmt.Errorf("inspect history migration destination: %w", err)
	}
	if err := requireMigrationParent(filepath.Dir(destination)); err != nil {
		return MigrationPlan{}, fmt.Errorf("inspect history migration destination parent: %w", err)
	}

	var plan MigrationPlan
	store := DirectoryStore{Path: source}
	err = store.withReadLock(ctx, func(root string) error {
		var buildErr error
		plan, buildErr = buildMigrationPlan(ctx, root, destination)
		return buildErr
	})
	if err != nil {
		return MigrationPlan{}, err
	}
	return plan, nil
}

// ApplyMigration publishes a stable copy only when confirmation matches the
// exact current source snapshot. A failed or killed copy cannot modify source.
func (s DirectoryStore) ApplyMigration(
	ctx context.Context,
	destination string,
	confirmation string,
) (MigrationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	source, destination, err := migrationPaths(s.Path, destination)
	if err != nil {
		return MigrationResult{}, err
	}
	confirmation = strings.TrimSpace(confirmation)
	if !model.IsSHA256Digest(confirmation) {
		return MigrationResult{}, fmt.Errorf("history migration confirmation must be a canonical sha256 digest")
	}
	parent := filepath.Dir(destination)
	if err := requireMigrationParent(parent); err != nil {
		return MigrationResult{}, fmt.Errorf("inspect history migration destination parent: %w", err)
	}

	var result MigrationResult
	store := DirectoryStore{
		Path:          source,
		migrationHook: s.migrationHook,
	}
	err = withMigrationParentLock(ctx, parent, func() error {
		return store.withReadLock(ctx, func(root string) error {
			plan, err := buildMigrationPlan(ctx, root, destination)
			if err != nil {
				return err
			}
			if plan.Digest != confirmation {
				return fmt.Errorf(
					"history migration confirmation %s does not match current plan digest %s",
					confirmation,
					plan.Digest,
				)
			}

			if _, err := os.Lstat(destination); err == nil {
				applied, verifyErr := verifyAppliedMigration(ctx, destination, plan)
				if verifyErr != nil {
					return verifyErr
				}
				result = migrationResult(plan, applied, true)
				return nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect history migration destination: %w", err)
			}

			stage := migrationStagePath(destination, plan.Digest)
			if stage == root || stage == destination ||
				pathContains(root, stage) || pathContains(stage, root) {
				return fmt.Errorf("history migration stage overlaps source or destination")
			}
			if err := resetMigrationStage(stage, plan); err != nil {
				return err
			}
			if err := copyMigrationStage(ctx, root, stage, plan, store.callMigrationHook); err != nil {
				return fmt.Errorf("build history migration stage %s: %w", stage, err)
			}
			verification, err := (DirectoryStore{Path: stage}).Verify(ctx)
			if err != nil {
				return fmt.Errorf("verify history migration stage: %w", err)
			}
			stageSnapshot, err := snapshotMigrationTree(ctx, stage)
			if err != nil {
				return fmt.Errorf("snapshot history migration stage: %w", err)
			}
			if stageSnapshot.PayloadDigest != plan.HistoricalPayloadDigest {
				return fmt.Errorf("history migration stage payload does not match confirmed plan")
			}
			if err := validateMigrationVerification(plan, verification); err != nil {
				return err
			}
			if err := store.callMigrationHook(migrationStepAfterStageCheck, 0); err != nil {
				return err
			}
			if _, err := os.Lstat(destination); err == nil {
				return fmt.Errorf("history migration destination appeared during publication")
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect history migration destination before publication: %w", err)
			}
			if err := os.Rename(stage, destination); err != nil {
				return fmt.Errorf("publish history migration destination: %w", err)
			}
			if err := syncDirectory(parent); err != nil {
				return fmt.Errorf("sync history migration destination parent: %w", err)
			}
			if err := store.callMigrationHook(migrationStepAfterPublish, 0); err != nil {
				return err
			}
			result = migrationResult(plan, verification, false)
			return nil
		})
	})
	if err != nil {
		return MigrationResult{}, err
	}
	if err := result.Validate(); err != nil {
		return MigrationResult{}, err
	}
	return result, nil
}

func buildMigrationPlan(
	ctx context.Context,
	root, destination string,
) (MigrationPlan, error) {
	metadata, err := readJSONFile[StoreMetadata](
		filepath.Join(root, "store.json"),
		MaxIdentityBytes,
	)
	if err != nil {
		return MigrationPlan{}, fmt.Errorf("read history store metadata: %w", err)
	}
	if err := metadata.validate(); err != nil {
		return MigrationPlan{}, err
	}
	if metadata.SchemaVersion == CurrentStoreSchemaVersion {
		return MigrationPlan{}, fmt.Errorf("history store already uses stable schema_version %q", CurrentStoreSchemaVersion)
	}
	if metadata.SchemaVersion != LegacyStoreSchemaVersion {
		return MigrationPlan{}, fmt.Errorf("history store schema_version %q cannot be migrated", metadata.SchemaVersion)
	}
	if _, err := os.Lstat(filepath.Join(root, migrationRecordFileName)); err == nil {
		return MigrationPlan{}, fmt.Errorf("legacy history store must not contain a stable migration record")
	} else if !errors.Is(err, os.ErrNotExist) {
		return MigrationPlan{}, fmt.Errorf("inspect history migration record: %w", err)
	}

	counts, err := inspectMigrationSource(root)
	if err != nil {
		return MigrationPlan{}, err
	}
	snapshot, err := snapshotMigrationTree(ctx, root)
	if err != nil {
		return MigrationPlan{}, err
	}
	plan := MigrationPlan{
		SchemaVersion:            CurrentMigrationPlanSchema,
		CompatibilityFloor:       PreGACompatibilityFloor,
		Source:                   root,
		Destination:              destination,
		SourceStoreSchemaVersion: metadata.SchemaVersion,
		TargetStoreSchemaVersion: CurrentStoreSchemaVersion,
		LayoutVersion:            metadata.LayoutVersion,
		SourceSnapshotDigest:     snapshot.Digest,
		HistoricalPayloadDigest:  snapshot.PayloadDigest,
		Files:                    snapshot.Files,
		Bytes:                    snapshot.Bytes,
		Runs:                     counts.Runs,
		Attempts:                 counts.Attempts,
		TerminalReports:          counts.TerminalReports,
		IncompleteAttempts:       counts.IncompleteAttempts,
		Events:                   counts.Events,
	}
	plan.Digest, err = migrationPlanDigest(plan)
	if err != nil {
		return MigrationPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return MigrationPlan{}, fmt.Errorf("validate history migration plan: %w", err)
	}
	return plan, nil
}

func inspectMigrationSource(root string) (migrationCounts, error) {
	records, err := readAllRuns(root)
	if err != nil {
		return migrationCounts{}, err
	}
	state, err := inspectRetentionState(root)
	if err != nil {
		return migrationCounts{}, err
	}
	if err := requireCleanRetentionState(state); err != nil {
		return migrationCounts{}, fmt.Errorf("history migration requires clean retention state: %w", err)
	}
	counts := migrationCounts{Runs: len(records)}
	for _, record := range records {
		counts.Attempts += len(record.Attempts)
		for _, attempt := range record.Attempts {
			counts.Events += len(attempt.Events)
			if attempt.Report == nil {
				counts.IncompleteAttempts++
			} else {
				counts.TerminalReports++
			}
		}
	}
	return counts, nil
}

func snapshotMigrationTree(
	ctx context.Context,
	root string,
) (migrationSnapshot, error) {
	fullHash := sha256.New()
	payloadHash := sha256.New()
	fullEncoder := json.NewEncoder(fullHash)
	payloadEncoder := json.NewEncoder(payloadHash)
	snapshot := migrationSnapshot{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if err := validateMigrationEntry(relative, info); err != nil {
			return err
		}
		if relative == historyStoreLockFileName {
			return nil
		}
		manifest := migrationManifestEntry{
			Path: relative,
			Mode: uint32(info.Mode().Perm()),
		}
		if info.IsDir() {
			manifest.Kind = "directory"
		} else {
			manifest.Kind = "file"
			manifest.Size = info.Size()
			digest, err := digestMigrationFile(ctx, path, info)
			if err != nil {
				return err
			}
			manifest.Digest = digest
			snapshot.Files++
			if snapshot.Files > MaxMigrationFiles {
				return fmt.Errorf("history migration exceeds maximum file count %d", MaxMigrationFiles)
			}
			if info.Size() > 0 && snapshot.Bytes > (1<<63-1)-info.Size() {
				return fmt.Errorf("history migration byte count overflows")
			}
			snapshot.Bytes += info.Size()
		}
		if err := fullEncoder.Encode(manifest); err != nil {
			return err
		}
		if relative != "store.json" && relative != migrationRecordFileName {
			payloadManifest := manifest
			if info.IsDir() {
				// Stable stores must remain writable after migration. Directory
				// modes are therefore canonicalized while immutable file bytes
				// and file modes remain part of the historical payload identity.
				payloadManifest.Mode = 0o700
			}
			if err := payloadEncoder.Encode(payloadManifest); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return migrationSnapshot{}, fmt.Errorf("snapshot history migration source: %w", err)
	}
	snapshot.Digest = "sha256:" + hex.EncodeToString(fullHash.Sum(nil))
	snapshot.PayloadDigest = "sha256:" + hex.EncodeToString(payloadHash.Sum(nil))
	return snapshot, nil
}

func validateMigrationEntry(relative string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("history migration entry %q must not be a symbolic link", relative)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf("history migration entry %q has unsupported file type", relative)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("history migration entry %q permissions %o are not private", relative, info.Mode().Perm())
	}
	if info.IsDir() && info.Mode().Perm()&0o500 != 0o500 {
		return fmt.Errorf("history migration directory %q must be owner-readable and searchable", relative)
	}
	if info.Mode().IsRegular() && info.Mode().Perm()&0o400 == 0 {
		return fmt.Errorf("history migration file %q must be owner-readable", relative)
	}
	parts := strings.Split(relative, "/")
	switch parts[0] {
	case historyStoreLockFileName, "store.json", migrationRecordFileName:
		if len(parts) != 1 || info.IsDir() {
			return fmt.Errorf("history migration contains invalid entry %q", relative)
		}
		return nil
	case "runs":
		return validateMigrationRunEntry(parts, info.IsDir())
	case retentionDirectoryName:
		return validateMigrationRetentionEntry(parts, info.IsDir())
	default:
		return fmt.Errorf("history migration contains unexpected entry %q", relative)
	}
}

func validateMigrationRunEntry(parts []string, directory bool) error {
	if len(parts) == 1 {
		if !directory {
			return fmt.Errorf("history migration runs entry must be a directory")
		}
		return nil
	}
	if !validHashDirectory(parts[1]) {
		return fmt.Errorf("history migration contains invalid run directory %q", parts[1])
	}
	if len(parts) == 2 {
		if !directory {
			return fmt.Errorf("history migration run entry must be a directory")
		}
		return nil
	}
	switch parts[2] {
	case "identity.json", "spec.json":
		if len(parts) != 3 || directory {
			return fmt.Errorf("history migration contains invalid run entry %q", strings.Join(parts, "/"))
		}
		return nil
	case "attempts":
		if len(parts) == 3 {
			if !directory {
				return fmt.Errorf("history migration attempts entry must be a directory")
			}
			return nil
		}
	default:
		return fmt.Errorf("history migration contains unexpected run entry %q", strings.Join(parts, "/"))
	}
	if len(parts) < 4 || !validHashDirectory(parts[3]) {
		return fmt.Errorf("history migration contains invalid attempt directory")
	}
	if len(parts) == 4 {
		if !directory {
			return fmt.Errorf("history migration attempt entry must be a directory")
		}
		return nil
	}
	switch parts[4] {
	case "identity.json", "summary.json", "report.json":
		if len(parts) != 5 || directory {
			return fmt.Errorf("history migration contains invalid attempt entry %q", strings.Join(parts, "/"))
		}
		return nil
	case "events":
		if len(parts) == 5 {
			if !directory {
				return fmt.Errorf("history migration events entry must be a directory")
			}
			return nil
		}
		if len(parts) == 6 && !directory {
			if _, ok := parseEventFileName(parts[5]); ok {
				return nil
			}
		}
	}
	return fmt.Errorf("history migration contains unexpected attempt entry %q", strings.Join(parts, "/"))
}

func validateMigrationRetentionEntry(parts []string, directory bool) error {
	if !directory {
		return fmt.Errorf("history migration requires empty retention maintenance directories")
	}
	if len(parts) == 1 {
		return nil
	}
	if len(parts) == 2 {
		switch parts[1] {
		case retentionOperationsDirectory, retentionTrashDirectory, retentionPendingDirectory:
			return nil
		}
	}
	return fmt.Errorf("history migration contains active or unexpected retention entry %q", strings.Join(parts, "/"))
}

func digestMigrationFile(
	ctx context.Context,
	path string,
	before os.FileInfo,
) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.CopyBuffer(
		hash,
		&migrationContextReader{ctx: ctx, reader: file},
		make([]byte, 32<<10),
	)
	stat, statErr := file.Stat()
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if statErr != nil {
		return "", statErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if !os.SameFile(before, stat) ||
		stat.Size() != before.Size() ||
		stat.Mode() != before.Mode() {
		return "", fmt.Errorf("history migration source file %s changed during snapshot", path)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func copyMigrationStage(
	ctx context.Context,
	source, stage string,
	plan MigrationPlan,
	hook func(string, int) error,
) error {
	if err := os.Mkdir(stage, 0o700); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(stage)); err != nil {
		return err
	}
	record := migrationRecord{
		SchemaVersion:            currentMigrationRecordSchema,
		CompatibilityFloor:       PreGACompatibilityFloor,
		PlanDigest:               plan.Digest,
		SourceStoreSchemaVersion: plan.SourceStoreSchemaVersion,
		SourceSnapshotDigest:     plan.SourceSnapshotDigest,
		HistoricalPayloadDigest:  plan.HistoricalPayloadDigest,
	}
	if err := writeMigrationJSON(ctx, stage, migrationRecordFileName, record); err != nil {
		return err
	}
	metadata := StoreMetadata{
		SchemaVersion: CurrentStoreSchemaVersion,
		LayoutVersion: CurrentLayoutVersion,
	}
	if err := writeMigrationJSON(ctx, stage, "store.json", metadata); err != nil {
		return err
	}
	if err := writeMigrationFile(ctx, filepath.Join(stage, historyStoreLockFileName), 0o600, nil); err != nil {
		return err
	}

	directories := []migrationDirectory{{path: stage, mode: 0o700}}
	fileIndex := 0
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == historyStoreLockFileName ||
			relative == "store.json" ||
			relative == migrationRecordFileName {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if err := validateMigrationEntry(relative, info); err != nil {
			return err
		}
		target := filepath.Join(stage, filepath.FromSlash(relative))
		if info.IsDir() {
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			directories = append(directories, migrationDirectory{
				path: target,
				mode: 0o700,
			})
			return nil
		}
		if err := copyMigrationFile(ctx, path, target, info); err != nil {
			return err
		}
		if err := hook(migrationStepAfterFileCopy, fileIndex); err != nil {
			return err
		}
		fileIndex++
		return nil
	})
	if err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		directory := directories[index]
		if directory.path != stage {
			if err := os.Chmod(directory.path, directory.mode); err != nil {
				return err
			}
		}
		if err := syncDirectory(directory.path); err != nil {
			return err
		}
	}
	return nil
}

func copyMigrationFile(
	ctx context.Context,
	source, target string,
	before os.FileInfo,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	opened, err := input.Stat()
	if err != nil {
		_ = input.Close()
		return err
	}
	if !os.SameFile(before, opened) ||
		opened.Mode() != before.Mode() ||
		opened.Size() != before.Size() {
		_ = input.Close()
		return fmt.Errorf("history migration source file %s changed before copy", source)
	}
	output, err := os.OpenFile(
		target,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		_ = input.Close()
		return err
	}
	written, copyErr := io.CopyBuffer(
		output,
		&migrationContextReader{ctx: ctx, reader: input},
		make([]byte, 32<<10),
	)
	after, statErr := input.Stat()
	inputCloseErr := input.Close()
	chmodErr := output.Chmod(before.Mode().Perm())
	syncErr := output.Sync()
	outputCloseErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if written != before.Size() {
		return fmt.Errorf(
			"history migration copied %d bytes from %s, expected %d",
			written,
			source,
			before.Size(),
		)
	}
	if statErr != nil {
		return statErr
	}
	if !os.SameFile(before, after) ||
		after.Mode() != before.Mode() ||
		after.Size() != before.Size() {
		return fmt.Errorf("history migration source file %s changed during copy", source)
	}
	if inputCloseErr != nil {
		return inputCloseErr
	}
	if chmodErr != nil {
		return chmodErr
	}
	if syncErr != nil {
		return syncErr
	}
	if outputCloseErr != nil {
		return outputCloseErr
	}
	return ctx.Err()
}

func writeMigrationJSON(ctx context.Context, directory, name string, value any) error {
	payload, err := marshalJSON(value, MaxIdentityBytes)
	if err != nil {
		return err
	}
	return writeMigrationFile(ctx, filepath.Join(directory, name), 0o600, payload)
}

func writeMigrationFile(
	ctx context.Context,
	path string,
	mode os.FileMode,
	payload []byte,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := file.Write(payload); err != nil {
			_ = file.Close()
			return err
		}
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func resetMigrationStage(stage string, plan MigrationPlan) error {
	info, err := os.Lstat(stage)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect history migration stage: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("history migration stage %s is not a private real directory", stage)
	}
	record, err := readJSONFile[migrationRecord](
		filepath.Join(stage, migrationRecordFileName),
		MaxIdentityBytes,
	)
	if err != nil {
		return fmt.Errorf(
			"history migration stage does not contain a valid ownership record: %w",
			err,
		)
	}
	if err := record.validate(plan); err != nil {
		return fmt.Errorf(
			"history migration stage ownership does not match confirmed plan: %w",
			err,
		)
	}
	if err := removePrivateTree(stage); err != nil {
		return fmt.Errorf("reset history migration stage: %w", err)
	}
	return nil
}

func verifyAppliedMigration(
	ctx context.Context,
	destination string,
	plan MigrationPlan,
) (VerificationResult, error) {
	if err := requireDirectory(destination); err != nil {
		return VerificationResult{}, fmt.Errorf("inspect existing history migration destination: %w", err)
	}
	record, err := readJSONFile[migrationRecord](
		filepath.Join(destination, migrationRecordFileName),
		MaxIdentityBytes,
	)
	if err != nil {
		return VerificationResult{}, fmt.Errorf("read history migration record: %w", err)
	}
	if err := record.validate(plan); err != nil {
		return VerificationResult{}, err
	}
	snapshot, err := snapshotMigrationTree(ctx, destination)
	if err != nil {
		return VerificationResult{}, err
	}
	if snapshot.PayloadDigest != plan.HistoricalPayloadDigest {
		return VerificationResult{}, fmt.Errorf("existing history migration destination payload does not match confirmed plan")
	}
	verification, err := (DirectoryStore{Path: destination}).Verify(ctx)
	if err != nil {
		return VerificationResult{}, fmt.Errorf("verify existing history migration destination: %w", err)
	}
	if err := validateMigrationVerification(plan, verification); err != nil {
		return VerificationResult{}, err
	}
	return verification, nil
}

func validateMigrationVerification(plan MigrationPlan, result VerificationResult) error {
	if result.StoreSchemaVersion != CurrentStoreSchemaVersion ||
		result.LayoutVersion != CurrentLayoutVersion ||
		result.MigrationRequired ||
		result.MaintenanceRequired ||
		result.MigrationPlanDigest != plan.Digest ||
		result.MigratedFromSchemaVersion != plan.SourceStoreSchemaVersion ||
		result.SourceSnapshotDigest != plan.SourceSnapshotDigest {
		return fmt.Errorf("history migration destination is not a clean stable store")
	}
	if result.Runs != plan.Runs ||
		result.Attempts != plan.Attempts ||
		result.TerminalReports != plan.TerminalReports ||
		result.IncompleteAttempts != plan.IncompleteAttempts ||
		result.Events != plan.Events {
		return fmt.Errorf("history migration destination counts do not match confirmed plan")
	}
	return nil
}

func migrationResult(
	plan MigrationPlan,
	verification VerificationResult,
	alreadyApplied bool,
) MigrationResult {
	return MigrationResult{
		SchemaVersion:        CurrentMigrationResultSchema,
		PlanDigest:           plan.Digest,
		Source:               plan.Source,
		Destination:          plan.Destination,
		SourceSnapshotDigest: plan.SourceSnapshotDigest,
		Files:                plan.Files,
		Bytes:                plan.Bytes,
		AlreadyApplied:       alreadyApplied,
		Verification:         verification,
	}
}

func (p MigrationPlan) Validate() error {
	if p.SchemaVersion != CurrentMigrationPlanSchema ||
		p.CompatibilityFloor != PreGACompatibilityFloor ||
		p.SourceStoreSchemaVersion != LegacyStoreSchemaVersion ||
		p.TargetStoreSchemaVersion != CurrentStoreSchemaVersion ||
		p.LayoutVersion != CurrentLayoutVersion {
		return fmt.Errorf("history migration plan version is unsupported")
	}
	if _, _, err := migrationPaths(p.Source, p.Destination); err != nil {
		return err
	}
	for name, digest := range map[string]string{
		"source_snapshot_digest":    p.SourceSnapshotDigest,
		"historical_payload_digest": p.HistoricalPayloadDigest,
		"digest":                    p.Digest,
	} {
		if !model.IsSHA256Digest(digest) {
			return fmt.Errorf("history migration plan %s must be a canonical sha256 digest", name)
		}
	}
	if p.Files < 1 || p.Files > MaxMigrationFiles || p.Bytes < 1 ||
		!countsWithinBounds(MaxRuns, p.Runs) ||
		!countsWithinBounds(
			MaxTotalAttempts,
			p.Attempts,
			p.TerminalReports,
			p.IncompleteAttempts,
		) ||
		p.Attempts > p.Runs*MaxAttemptsPerRun ||
		p.TerminalReports+p.IncompleteAttempts != p.Attempts ||
		!countsWithinBounds(MaxEventsPerRun*p.Runs, p.Events) {
		return fmt.Errorf("history migration plan counts are inconsistent")
	}
	want, err := migrationPlanDigest(p)
	if err != nil {
		return err
	}
	if p.Digest != want {
		return fmt.Errorf("history migration plan digest %s does not match canonical digest %s", p.Digest, want)
	}
	return nil
}

func (r MigrationResult) Validate() error {
	if r.SchemaVersion != CurrentMigrationResultSchema {
		return fmt.Errorf("history migration result schema_version must be %q", CurrentMigrationResultSchema)
	}
	if !model.IsSHA256Digest(r.PlanDigest) ||
		!model.IsSHA256Digest(r.SourceSnapshotDigest) ||
		r.Files < 1 ||
		r.Files > MaxMigrationFiles ||
		r.Bytes < 1 {
		return fmt.Errorf("history migration result identity is invalid")
	}
	if _, _, err := migrationPaths(r.Source, r.Destination); err != nil {
		return err
	}
	if err := r.Verification.Validate(); err != nil {
		return err
	}
	if r.Verification.MigrationPlanDigest != r.PlanDigest ||
		r.Verification.SourceSnapshotDigest != r.SourceSnapshotDigest ||
		r.Verification.StoreSchemaVersion != CurrentStoreSchemaVersion ||
		r.Verification.MigrationRequired {
		return fmt.Errorf("history migration result verification does not match migration identity")
	}
	return nil
}

func (r migrationRecord) validate(plan MigrationPlan) error {
	if err := r.validateStandalone(); err != nil {
		return err
	}
	want := migrationRecord{
		SchemaVersion:            currentMigrationRecordSchema,
		CompatibilityFloor:       PreGACompatibilityFloor,
		PlanDigest:               plan.Digest,
		SourceStoreSchemaVersion: plan.SourceStoreSchemaVersion,
		SourceSnapshotDigest:     plan.SourceSnapshotDigest,
		HistoricalPayloadDigest:  plan.HistoricalPayloadDigest,
	}
	if r != want {
		return fmt.Errorf("history migration record does not match confirmed plan")
	}
	return nil
}

func (r migrationRecord) validateStandalone() error {
	if r.SchemaVersion != currentMigrationRecordSchema ||
		r.CompatibilityFloor != PreGACompatibilityFloor ||
		r.SourceStoreSchemaVersion != LegacyStoreSchemaVersion {
		return fmt.Errorf("history migration record version is unsupported")
	}
	for name, digest := range map[string]string{
		"plan_digest":               r.PlanDigest,
		"source_snapshot_digest":    r.SourceSnapshotDigest,
		"historical_payload_digest": r.HistoricalPayloadDigest,
	} {
		if !model.IsSHA256Digest(digest) {
			return fmt.Errorf("history migration record %s must be a canonical sha256 digest", name)
		}
	}
	return nil
}

func migrationPlanDigest(plan MigrationPlan) (string, error) {
	copy := plan
	copy.Digest = ""
	payload, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("encode history migration plan digest: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func migrationPaths(source, destination string) (string, string, error) {
	source = strings.TrimSpace(source)
	destination = strings.TrimSpace(destination)
	if source == "" {
		return "", "", fmt.Errorf("history migration source store path is required")
	}
	if destination == "" {
		return "", "", fmt.Errorf("history migration destination path is required")
	}
	source, err := filepath.Abs(filepath.Clean(source))
	if err != nil {
		return "", "", fmt.Errorf("resolve history migration source: %w", err)
	}
	sourcePath := source
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return "", "", fmt.Errorf("resolve physical history migration source: %w", err)
	}
	destination, err = filepath.Abs(filepath.Clean(destination))
	if err != nil {
		return "", "", fmt.Errorf("resolve history migration destination: %w", err)
	}
	if err := requireDestinationAncestors(sourcePath, filepath.Dir(destination)); err != nil {
		return "", "", err
	}
	destinationParent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return "", "", fmt.Errorf("resolve physical history migration destination parent: %w", err)
	}
	destination = filepath.Join(destinationParent, filepath.Base(destination))
	if source == destination {
		return "", "", fmt.Errorf("history migration destination must differ from source")
	}
	if pathContains(source, destination) || pathContains(destination, source) {
		return "", "", fmt.Errorf("history migration source and destination must not contain each other")
	}
	return source, destination, nil
}

func requireDestinationAncestors(source, destinationParent string) error {
	current := destinationParent
	for {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect history migration destination ancestor %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !pathContains(current, source) {
				return fmt.Errorf(
					"history migration destination ancestor %s must not be a symbolic link",
					current,
				)
			}
		} else if !info.IsDir() {
			return fmt.Errorf("history migration destination ancestor %s is not a directory", current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil || relative == "." || relative == ".." {
		return relative == "."
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func migrationStagePath(destination, digest string) string {
	return filepath.Join(
		filepath.Dir(destination),
		"."+filepath.Base(destination)+".pgdrill-migration-"+strings.TrimPrefix(digest, "sha256:")[:16],
	)
}

func withMigrationParentLock(
	ctx context.Context,
	parent string,
	operation func() error,
) error {
	path := filepath.Join(parent, migrationParentLockFileName)
	lock, err := filelock.OpenPrivate(path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("open history migration parent lock: %w", err)
	}
	defer lock.Close()
	if err := filelock.Lock(ctx, lock, filelock.Exclusive); err != nil {
		return fmt.Errorf("lock history migration destination parent: %w", err)
	}
	defer filelock.Unlock(lock) //nolint:errcheck
	return operation()
}

func requireMigrationParent(path string) error {
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	current := path
	for {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%s is not a real directory", current)
		}
		if current == path && info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s permissions %o permit untrusted writes", path, info.Mode().Perm())
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return nil
}

func (s DirectoryStore) callMigrationHook(step string, index int) error {
	if s.migrationHook == nil {
		return nil
	}
	return s.migrationHook(step, index)
}

type migrationContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *migrationContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
