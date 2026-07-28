package history

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestDirectoryStoreMigratesCompatibilityFloorWithoutRewritingHistory(t *testing.T) {
	t.Parallel()

	source := extractHistoryFixture(
		t,
		filepath.Join("testdata", PreGACompatibilityFloor, "history-store.tar.gz"),
	)
	destination := filepath.Join(filepath.Dir(source), "history-stable")
	store := DirectoryStore{Path: source}

	sourceBefore, err := snapshotMigrationTree(context.Background(), source)
	if err != nil {
		t.Fatalf("snapshot source before migration: %v", err)
	}
	verification, err := store.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() legacy source error = %v", err)
	}
	if !verification.MigrationRequired ||
		verification.StoreSchemaVersion != LegacyStoreSchemaVersion {
		t.Fatalf("legacy verification = %#v", verification)
	}

	plan, err := store.PlanMigration(context.Background(), destination)
	if err != nil {
		t.Fatalf("PlanMigration() error = %v", err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("plan.Validate() error = %v", err)
	}
	again, err := store.PlanMigration(context.Background(), destination)
	if err != nil {
		t.Fatalf("second PlanMigration() error = %v", err)
	}
	if !reflect.DeepEqual(again, plan) {
		t.Fatalf("PlanMigration() is not deterministic:\nfirst: %#v\nsecond: %#v", plan, again)
	}
	if plan.SourceSnapshotDigest != sourceBefore.Digest ||
		plan.HistoricalPayloadDigest != sourceBefore.PayloadDigest ||
		plan.Runs != 2 ||
		plan.Attempts != 2 ||
		plan.TerminalReports != 2 ||
		plan.Events != 52 {
		t.Fatalf("migration plan = %#v", plan)
	}

	if _, err := store.ApplyMigration(
		context.Background(),
		destination,
		"sha256:"+strings.Repeat("0", 64),
	); err == nil || !strings.Contains(err.Error(), "does not match current plan digest") {
		t.Fatalf("ApplyMigration() wrong confirmation error = %v", err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination exists after rejected confirmation: %v", err)
	}

	result, err := store.ApplyMigration(context.Background(), destination, plan.Digest)
	if err != nil {
		t.Fatalf("ApplyMigration() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result.Validate() error = %v", err)
	}
	if result.AlreadyApplied ||
		result.Verification.StoreSchemaVersion != CurrentStoreSchemaVersion ||
		result.Verification.MigrationRequired ||
		result.Verification.MigrationPlanDigest != plan.Digest ||
		result.Verification.MigratedFromSchemaVersion != LegacyStoreSchemaVersion ||
		result.Verification.SourceSnapshotDigest != plan.SourceSnapshotDigest {
		t.Fatalf("migration result = %#v", result)
	}

	sourceAfter, err := snapshotMigrationTree(context.Background(), source)
	if err != nil {
		t.Fatalf("snapshot source after migration: %v", err)
	}
	if sourceAfter != sourceBefore {
		t.Fatalf("source changed during migration:\nbefore: %#v\nafter: %#v", sourceBefore, sourceAfter)
	}
	destinationSnapshot, err := snapshotMigrationTree(context.Background(), destination)
	if err != nil {
		t.Fatalf("snapshot destination: %v", err)
	}
	if destinationSnapshot.PayloadDigest != sourceBefore.PayloadDigest {
		t.Fatalf(
			"destination historical payload digest = %s, want %s",
			destinationSnapshot.PayloadDigest,
			sourceBefore.PayloadDigest,
		)
	}
	assertHistoryPayloadEqual(t, source, destination)

	retry, err := store.ApplyMigration(context.Background(), destination, plan.Digest)
	if err != nil {
		t.Fatalf("ApplyMigration() retry error = %v", err)
	}
	if !retry.AlreadyApplied {
		t.Fatalf("ApplyMigration() retry = %#v, want already applied", retry)
	}
}

func TestMigrationPlanValidateRejectsCountsBeyondRunCapacity(t *testing.T) {
	t.Parallel()

	source := extractHistoryFixture(
		t,
		filepath.Join("testdata", PreGACompatibilityFloor, "history-store.tar.gz"),
	)
	store := DirectoryStore{Path: source}
	plan, err := store.PlanMigration(
		context.Background(),
		filepath.Join(filepath.Dir(source), "history-stable"),
	)
	if err != nil {
		t.Fatal(err)
	}
	plan.Runs = 0
	digest, err := migrationPlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Digest = digest
	if err := plan.Validate(); err == nil ||
		!strings.Contains(err.Error(), "counts are inconsistent") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestDirectoryStoreMigrationResumesAfterInterruptedCopy(t *testing.T) {
	t.Parallel()

	source := extractHistoryFixture(
		t,
		filepath.Join("testdata", PreGACompatibilityFloor, "history-store.tar.gz"),
	)
	destination := filepath.Join(filepath.Dir(source), "history-stable")
	base := DirectoryStore{Path: source}
	plan, err := base.PlanMigration(context.Background(), destination)
	if err != nil {
		t.Fatalf("PlanMigration() error = %v", err)
	}
	before, err := snapshotMigrationTree(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected migration interruption")
	interrupted := DirectoryStore{
		Path: source,
		migrationHook: func(step string, index int) error {
			if step == migrationStepAfterFileCopy && index == 3 {
				return injected
			}
			return nil
		},
	}
	if _, err := interrupted.ApplyMigration(
		context.Background(),
		destination,
		plan.Digest,
	); !errors.Is(err, injected) {
		t.Fatalf("ApplyMigration() interrupted error = %v", err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination exists after interrupted copy: %v", err)
	}
	after, err := snapshotMigrationTree(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("source changed after interrupted migration")
	}
	stage := migrationStagePath(destination, plan.Digest)
	if err := requireDirectory(stage); err != nil {
		t.Fatalf("interrupted stage is unavailable: %v", err)
	}

	result, err := base.ApplyMigration(context.Background(), destination, plan.Digest)
	if err != nil {
		t.Fatalf("ApplyMigration() resume error = %v", err)
	}
	if result.AlreadyApplied {
		t.Fatalf("resumed copy unexpectedly reported already applied")
	}
	if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage remains after publication: %v", err)
	}
}

func TestDirectoryStoreMigrationRejectsStalePlanAndLegacyWrites(t *testing.T) {
	t.Parallel()

	source := extractHistoryFixture(
		t,
		filepath.Join("testdata", PreGACompatibilityFloor, "history-store.tar.gz"),
	)
	destination := filepath.Join(filepath.Dir(source), "history-stable")
	store := DirectoryStore{Path: source}
	plan, err := store.PlanMigration(context.Background(), destination)
	if err != nil {
		t.Fatalf("PlanMigration() error = %v", err)
	}

	reportPath := firstHistoryFile(t, source, "report.json")
	payload, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(reportPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyMigration(
		context.Background(),
		destination,
		plan.Digest,
	); err == nil || !strings.Contains(err.Error(), "does not match current plan digest") {
		t.Fatalf("ApplyMigration() stale confirmation error = %v", err)
	}

	summaries, err := store.List(context.Background())
	if err != nil || len(summaries) == 0 {
		t.Fatalf("List() legacy source = %#v, %v", summaries, err)
	}
	record, err := store.ShowAttempt(
		context.Background(),
		summaries[0].RunID,
		summaries[0].AttemptID,
	)
	if err != nil || len(record.Attempts) != 1 || record.Attempts[0].Report == nil {
		t.Fatalf("ShowAttempt() legacy source = %#v, %v", record, err)
	}
	current := validResult(t, "legacy-store-write", "attempt-1", model.DrillStatusPassed)
	if err := store.SaveReport(context.Background(), current); err == nil ||
		!strings.Contains(err.Error(), "read-only") {
		t.Fatalf("SaveReport() legacy store error = %v", err)
	}
}

func TestDirectoryStoreMigrationPublishesStableWritableStore(t *testing.T) {
	t.Parallel()

	source := extractHistoryFixture(
		t,
		filepath.Join("testdata", PreGACompatibilityFloor, "history-store.tar.gz"),
	)
	destination := filepath.Join(filepath.Dir(source), "history-stable")
	store := DirectoryStore{Path: source}
	plan, err := store.PlanMigration(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyMigration(context.Background(), destination, plan.Digest); err != nil {
		t.Fatal(err)
	}

	stable := DirectoryStore{Path: destination}
	legacyRuns, err := store.List(context.Background())
	if err != nil || len(legacyRuns) == 0 {
		t.Fatalf("List() legacy runs = %#v, %v", legacyRuns, err)
	}
	legacyRetry := model.RunEvent{
		SchemaVersion: model.CurrentRunEventSchemaVersion,
		RunID:         legacyRuns[0].RunID,
		AttemptID:     "stable-retry",
		SpecDigest:    legacyRuns[0].SpecDigest,
		Sequence:      1,
		Type:          model.RunEventStarted,
		OccurredAt:    time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
	}
	if err := stable.WriteEvent(context.Background(), legacyRetry); err == nil ||
		!strings.Contains(err.Error(), "different content") {
		t.Fatalf("WriteEvent() legacy logical-run retry error = %v", err)
	}

	result := validResult(t, "stable-run", "attempt-1", model.DrillStatusPassed)
	for _, event := range validEvents(result) {
		if err := stable.WriteEvent(context.Background(), event); err != nil {
			t.Fatalf("WriteEvent() stable destination error = %v", err)
		}
	}
	if err := stable.SaveReport(context.Background(), result); err != nil {
		t.Fatalf("SaveReport() stable destination error = %v", err)
	}
	verification, err := stable.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() stable destination error = %v", err)
	}
	if verification.Runs != plan.Runs+1 ||
		verification.Attempts != plan.Attempts+1 ||
		verification.StoreSchemaVersion != CurrentStoreSchemaVersion {
		t.Fatalf("stable destination verification = %#v", verification)
	}
}

func TestDirectoryStoreMigrationPreservesCleanRetentionLayout(t *testing.T) {
	t.Parallel()

	source := extractHistoryFixture(
		t,
		filepath.Join("testdata", PreGACompatibilityFloor, "history-store.tar.gz"),
	)
	operations, trash, pending, err := ensureRetentionBase(source)
	if err != nil {
		t.Fatalf("initialize clean retention layout: %v", err)
	}
	destination := filepath.Join(filepath.Dir(source), "history-stable")
	store := DirectoryStore{Path: source}
	plan, err := store.PlanMigration(context.Background(), destination)
	if err != nil {
		t.Fatalf("PlanMigration() clean retention error = %v", err)
	}
	result, err := store.ApplyMigration(context.Background(), destination, plan.Digest)
	if err != nil {
		t.Fatalf("ApplyMigration() clean retention error = %v", err)
	}
	if result.Verification.MaintenanceRequired {
		t.Fatalf("clean retention migration requires maintenance: %#v", result.Verification)
	}
	for _, sourcePath := range []string{operations, trash, pending} {
		relative, err := filepath.Rel(source, sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := requireDirectory(filepath.Join(destination, relative)); err != nil {
			t.Fatalf("migrated retention directory %q: %v", relative, err)
		}
	}
}

func TestDirectoryStoreMigrationPreservesReadOnlyDirectoryMode(t *testing.T) {
	t.Parallel()

	source := extractHistoryFixture(
		t,
		filepath.Join("testdata", PreGACompatibilityFloor, "history-store.tar.gz"),
	)
	sourceRuns := filepath.Join(source, "runs")
	if err := os.Chmod(sourceRuns, 0o500); err != nil {
		t.Fatal(err)
	}
	sourceReport := firstHistoryFile(t, source, "report.json")
	if err := os.Chmod(sourceReport, 0o400); err != nil {
		t.Fatal(err)
	}
	reportRelative, err := filepath.Rel(source, sourceReport)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(filepath.Dir(source), "history-stable")
	defer func() {
		_ = os.Chmod(sourceRuns, 0o700)
		_ = os.Chmod(sourceReport, 0o600)
		_ = os.Chmod(filepath.Join(destination, "runs"), 0o700)
	}()

	store := DirectoryStore{Path: source}
	plan, err := store.PlanMigration(context.Background(), destination)
	if err != nil {
		t.Fatalf("PlanMigration() read-only directory error = %v", err)
	}
	if _, err := store.ApplyMigration(context.Background(), destination, plan.Digest); err != nil {
		t.Fatalf("ApplyMigration() read-only directory error = %v", err)
	}
	info, err := os.Lstat(filepath.Join(destination, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o500 {
		t.Fatalf("migrated runs permissions = %o, want 500", info.Mode().Perm())
	}
	info, err = os.Lstat(filepath.Join(destination, reportRelative))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("migrated report permissions = %o, want 400", info.Mode().Perm())
	}
}

func TestDirectoryStoreMigrationRecoversAfterKilledProcess(t *testing.T) {
	source := extractHistoryFixture(
		t,
		filepath.Join("testdata", PreGACompatibilityFloor, "history-store.tar.gz"),
	)
	destination := filepath.Join(filepath.Dir(source), "history-stable")
	ready := filepath.Join(filepath.Dir(source), "migration-helper.ready")
	store := DirectoryStore{Path: source}
	plan, err := store.PlanMigration(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	before, err := snapshotMigrationTree(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestHistoryMigrationKilledProcessHelper$")
	command.Env = append(
		os.Environ(),
		"PGDRILL_MIGRATION_HELPER=1",
		"PGDRILL_MIGRATION_SOURCE="+source,
		"PGDRILL_MIGRATION_DESTINATION="+destination,
		"PGDRILL_MIGRATION_DIGEST="+plan.Digest,
		"PGDRILL_MIGRATION_READY="+ready,
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Lstat(ready); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect migration helper readiness: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("migration helper did not reach copy hook:\n%s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill migration helper: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed migration helper exited successfully")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination exists after killed copy: %v", err)
	}
	after, err := snapshotMigrationTree(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("source changed after killed migration process")
	}

	result, err := store.ApplyMigration(context.Background(), destination, plan.Digest)
	if err != nil {
		t.Fatalf("ApplyMigration() after killed process error = %v", err)
	}
	if result.AlreadyApplied {
		t.Fatalf("recovered migration unexpectedly reported already applied")
	}
}

func TestHistoryMigrationKilledProcessHelper(t *testing.T) {
	if os.Getenv("PGDRILL_MIGRATION_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	store := DirectoryStore{
		Path: os.Getenv("PGDRILL_MIGRATION_SOURCE"),
		migrationHook: func(step string, index int) error {
			if step != migrationStepAfterFileCopy || index != 0 {
				return nil
			}
			if err := os.WriteFile(
				os.Getenv("PGDRILL_MIGRATION_READY"),
				[]byte("ready\n"),
				0o600,
			); err != nil {
				return err
			}
			select {}
		},
	}
	_, err := store.ApplyMigration(
		context.Background(),
		os.Getenv("PGDRILL_MIGRATION_DESTINATION"),
		os.Getenv("PGDRILL_MIGRATION_DIGEST"),
	)
	t.Fatalf("migration helper returned unexpectedly: %v", err)
}

func TestDirectoryStoreMigrationRejectsUnsafeState(t *testing.T) {
	t.Run("unexpected source entry", func(t *testing.T) {
		source := extractHistoryFixture(
			t,
			filepath.Join("testdata", PreGACompatibilityFloor, "history-store.tar.gz"),
		)
		if err := os.WriteFile(filepath.Join(source, "unexpected"), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := (DirectoryStore{Path: source}).PlanMigration(
			context.Background(),
			filepath.Join(filepath.Dir(source), "stable"),
		)
		if err == nil || !strings.Contains(err.Error(), "unexpected entry") {
			t.Fatalf("PlanMigration() unexpected source error = %v", err)
		}
	})

	t.Run("symbolic link stage", func(t *testing.T) {
		source := extractHistoryFixture(
			t,
			filepath.Join("testdata", PreGACompatibilityFloor, "history-store.tar.gz"),
		)
		destination := filepath.Join(filepath.Dir(source), "stable")
		store := DirectoryStore{Path: source}
		plan, err := store.PlanMigration(context.Background(), destination)
		if err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		sentinel := filepath.Join(outside, "sentinel")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, migrationStagePath(destination, plan.Digest)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ApplyMigration(
			context.Background(),
			destination,
			plan.Digest,
		); err == nil || !strings.Contains(err.Error(), "not a private real directory") {
			t.Fatalf("ApplyMigration() symbolic stage error = %v", err)
		}
		if payload, err := os.ReadFile(sentinel); err != nil || string(payload) != "keep" {
			t.Fatalf("symbolic stage target changed: %q, %v", payload, err)
		}
	})

	t.Run("destination collision", func(t *testing.T) {
		source := extractHistoryFixture(
			t,
			filepath.Join("testdata", PreGACompatibilityFloor, "history-store.tar.gz"),
		)
		destination := filepath.Join(filepath.Dir(source), "stable")
		store := DirectoryStore{Path: source}
		plan, err := store.PlanMigration(context.Background(), destination)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(destination, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ApplyMigration(
			context.Background(),
			destination,
			plan.Digest,
		); err == nil || !strings.Contains(err.Error(), "migration record") {
			t.Fatalf("ApplyMigration() destination collision error = %v", err)
		}
	})

	t.Run("nested destination", func(t *testing.T) {
		source := extractHistoryFixture(
			t,
			filepath.Join("testdata", PreGACompatibilityFloor, "history-store.tar.gz"),
		)
		_, err := (DirectoryStore{Path: source}).PlanMigration(
			context.Background(),
			filepath.Join(source, "stable"),
		)
		if err == nil || !strings.Contains(err.Error(), "must not contain each other") {
			t.Fatalf("PlanMigration() nested destination error = %v", err)
		}
	})
}

func assertHistoryPayloadEqual(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(filepath.Join(source, "runs"), func(
		path string,
		entry os.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		sourcePayload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		destinationPayload, err := os.ReadFile(filepath.Join(destination, relative))
		if err != nil {
			return err
		}
		if !bytes.Equal(sourcePayload, destinationPayload) {
			return errors.New("historical file changed: " + relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func firstHistoryFile(t *testing.T, root, base string) string {
	t.Helper()
	var found string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == base {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == "" {
		t.Fatalf("history file %q was not found", base)
	}
	return found
}
