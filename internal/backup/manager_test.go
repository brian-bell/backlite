package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/store"
)

func TestRunBackup_CreatesVerifiableCompressedArtifactFromLiveDatabase(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "backlite.db")
	backupDir := filepath.Join(root, "backups")

	s, err := store.NewSQLite(ctx, dbPath, filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("NewSQLite() error = %v", err)
	}
	defer s.Close()

	now := time.Date(2026, 4, 25, 12, 34, 56, 0, time.UTC)
	task := &models.Task{
		ID:        "bf_backup_test",
		Status:    models.TaskStatusPending,
		TaskMode:  models.TaskModeCode,
		Harness:   models.HarnessClaudeCode,
		RepoURL:   "https://github.com/test/repo",
		Branch:    "backlite/test",
		Prompt:    "exercise backup worker",
		Model:     "claude-sonnet-4-6",
		CreatePR:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	m := New(Config{
		Enabled:      true,
		DatabasePath: dbPath,
		Directory:    backupDir,
		Interval:     24 * time.Hour,
	})

	if err := m.runBackup(ctx, now); err != nil {
		t.Fatalf("runBackup() error = %v", err)
	}

	artifactPath := filepath.Join(backupDir, "backlite-20260425T123456Z.sqlite.gz")
	metadataPath := metadataPath(artifactPath)

	if _, err := os.Stat(artifactPath); err != nil {
		t.Fatalf("artifact missing: %v", err)
	}
	if _, err := os.Stat(metadataPath); err != nil {
		t.Fatalf("metadata missing: %v", err)
	}

	meta, valid, err := readMetadata(artifactPath, metadataPath)
	if err != nil {
		t.Fatalf("readMetadata() error = %v", err)
	}
	if !valid {
		t.Fatal("readMetadata() reported artifact invalid, want valid finalized backup")
	}
	if meta.FileName != filepath.Base(artifactPath) {
		t.Fatalf("metadata file_name = %q, want %q", meta.FileName, filepath.Base(artifactPath))
	}
	if meta.SizeBytes <= 0 {
		t.Fatalf("metadata size_bytes = %d, want > 0", meta.SizeBytes)
	}
	if meta.SHA256 == "" {
		t.Fatal("metadata sha256 is empty")
	}
	if meta.FinalizedAt.IsZero() {
		t.Fatal("metadata finalized_at is zero, want non-zero finalization timestamp")
	}
	if meta.FinalizedAt.Before(meta.CreatedAt) {
		t.Fatalf("metadata finalized_at = %v before created_at = %v", meta.FinalizedAt, meta.CreatedAt)
	}
	if err := verifyArtifactChecksum(artifactPath, meta.SHA256); err != nil {
		t.Fatalf("verifyArtifactChecksum() error = %v", err)
	}

	verifyPath := filepath.Join(root, "verify.sqlite")
	if err := decompressFile(artifactPath, verifyPath); err != nil {
		t.Fatalf("decompressFile() error = %v", err)
	}
	if err := validateSQLiteDatabase(ctx, verifyPath); err != nil {
		t.Fatalf("validateSQLiteDatabase() error = %v", err)
	}

	db, err := sql.Open("sqlite", verifyPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks").Scan(&count); err != nil {
		t.Fatalf("query backup task count: %v", err)
	}
	if count != 1 {
		t.Fatalf("backup task count = %d, want 1", count)
	}
}

func TestNeedsBackup_IgnoresPartialArtifacts(t *testing.T) {
	dir := t.TempDir()

	oldTime := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	oldArtifact := filepath.Join(dir, "backlite-20260425T100000Z.sqlite.gz")
	if err := writeValidTestArtifact(t, oldArtifact, oldTime, []byte("old-backup")); err != nil {
		t.Fatalf("writeValidTestArtifact(old) error = %v", err)
	}

	partialArtifact := filepath.Join(dir, "backlite-20260425T115500Z.sqlite.gz")
	if err := os.WriteFile(partialArtifact, []byte("partial"), 0o600); err != nil {
		t.Fatalf("WriteFile(partialArtifact) error = %v", err)
	}
	if err := os.WriteFile(partialArtifact+".tmp", []byte("partial"), 0o600); err != nil {
		t.Fatalf("WriteFile(temp partial) error = %v", err)
	}

	m := New(Config{
		Enabled:   true,
		Directory: dir,
		Interval:  time.Hour,
	})
	m.now = func() time.Time {
		return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	}

	due, latest, err := m.needsBackup()
	if err != nil {
		t.Fatalf("needsBackup() error = %v", err)
	}
	if !due {
		t.Fatal("needsBackup() = false, want true because only finalized backup is two hours old")
	}
	if latest == nil {
		t.Fatal("latest artifact = nil, want oldest finalized backup")
	}
	if got := filepath.Base(latest.Path); got != filepath.Base(oldArtifact) {
		t.Fatalf("latest artifact = %q, want %q", got, filepath.Base(oldArtifact))
	}
}

func TestNeedsBackup_SkipsArtifactWithMismatchedSHA256(t *testing.T) {
	dir := t.TempDir()

	artifactPath := filepath.Join(dir, "backlite-20260425T100000Z.sqlite.gz")
	if err := writeValidTestArtifact(t, artifactPath, time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC), []byte("original-bytes")); err != nil {
		t.Fatalf("writeValidTestArtifact() error = %v", err)
	}
	// Corrupt the artifact in place without updating the sidecar. Same byte
	// length, different content — exactly the case the structural-only
	// check would let through.
	if err := os.WriteFile(artifactPath, []byte("corrupted-byt!"), 0o600); err != nil {
		t.Fatalf("corrupt WriteFile error = %v", err)
	}

	m := New(Config{
		Enabled:   true,
		Directory: dir,
		Interval:  time.Hour,
	})
	m.now = func() time.Time {
		return time.Date(2026, 4, 25, 11, 0, 0, 0, time.UTC)
	}

	due, latest, err := m.needsBackup()
	if err != nil {
		t.Fatalf("needsBackup() error = %v", err)
	}
	if !due {
		t.Fatal("needsBackup() = false, want true when the only artifact's checksum does not match")
	}
	if latest != nil {
		t.Fatalf("latest = %+v, want nil because the corrupted artifact must not be trusted", latest)
	}
}

func TestNeedsBackup_FallsBackToOlderValidArtifactWhenNewerCorrupted(t *testing.T) {
	dir := t.TempDir()

	oldTime := time.Date(2026, 4, 25, 8, 0, 0, 0, time.UTC)
	oldArtifact := filepath.Join(dir, "backlite-20260425T080000Z.sqlite.gz")
	if err := writeValidTestArtifact(t, oldArtifact, oldTime, []byte("old-good")); err != nil {
		t.Fatalf("writeValidTestArtifact(old) error = %v", err)
	}

	newTime := time.Date(2026, 4, 25, 11, 0, 0, 0, time.UTC)
	newerArtifact := filepath.Join(dir, "backlite-20260425T110000Z.sqlite.gz")
	if err := writeValidTestArtifact(t, newerArtifact, newTime, []byte("new-good")); err != nil {
		t.Fatalf("writeValidTestArtifact(new) error = %v", err)
	}
	// Corrupt the newer artifact's bytes after writing the sidecar.
	if err := os.WriteFile(newerArtifact, []byte("new-bad!"), 0o600); err != nil {
		t.Fatalf("corrupt WriteFile error = %v", err)
	}

	m := New(Config{
		Enabled:   true,
		Directory: dir,
		Interval:  time.Hour,
	})
	m.now = func() time.Time {
		return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	}

	due, latest, err := m.needsBackup()
	if err != nil {
		t.Fatalf("needsBackup() error = %v", err)
	}
	if !due {
		t.Fatal("needsBackup() = false, want true (older valid backup is 4 hours old, interval 1 hour)")
	}
	if latest == nil {
		t.Fatal("latest = nil, want the older valid artifact")
	}
	if got := filepath.Base(latest.Path); got != filepath.Base(oldArtifact) {
		t.Fatalf("latest = %q, want fallback to older valid artifact %q", got, filepath.Base(oldArtifact))
	}
}

func TestNeedsBackup_NotDueWhenLatestArtifactIsFresh(t *testing.T) {
	dir := t.TempDir()

	finalized := time.Date(2026, 4, 25, 11, 30, 0, 0, time.UTC)
	artifactPath := filepath.Join(dir, "backlite-20260425T112800Z.sqlite.gz")
	if err := writeValidTestArtifactFinalizedAt(t, artifactPath, finalized.Add(-2*time.Minute), finalized, []byte("fresh-backup")); err != nil {
		t.Fatalf("writeValidTestArtifactFinalizedAt() error = %v", err)
	}

	m := New(Config{
		Enabled:   true,
		Directory: dir,
		Interval:  time.Hour,
	})
	m.now = func() time.Time {
		return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	}

	due, latest, err := m.needsBackup()
	if err != nil {
		t.Fatalf("needsBackup() error = %v", err)
	}
	if due {
		t.Fatal("needsBackup() = true, want false (latest backup finalized 30 minutes ago, interval 1 hour)")
	}
	if latest == nil {
		t.Fatal("latest = nil, want the fresh artifact")
	}
}

func TestNeedsBackup_UsesFinalizedAtForAgeComparison(t *testing.T) {
	// Locks in the fix for the age-comparison bug: the filename / scheduled
	// timestamp can be hours older than `now` while the backup is still fresh
	// because finalization just completed. Without `FinalizedAt`-based aging,
	// a backup that takes longer than the interval would immediately appear
	// stale and the scheduler would loop continuously.
	dir := t.TempDir()

	scheduled := time.Date(2026, 4, 25, 9, 0, 0, 0, time.UTC)
	finalized := time.Date(2026, 4, 25, 11, 45, 0, 0, time.UTC)
	artifactPath := filepath.Join(dir, "backlite-20260425T090000Z.sqlite.gz")
	if err := writeValidTestArtifactFinalizedAt(t, artifactPath, scheduled, finalized, []byte("slow-backup")); err != nil {
		t.Fatalf("writeValidTestArtifactFinalizedAt() error = %v", err)
	}

	m := New(Config{
		Enabled:   true,
		Directory: dir,
		Interval:  time.Hour,
	})
	m.now = func() time.Time {
		return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	}

	due, _, err := m.needsBackup()
	if err != nil {
		t.Fatalf("needsBackup() error = %v", err)
	}
	if due {
		t.Fatal("needsBackup() = true, want false (finalized 15 minutes ago, even though scheduled 3 hours ago)")
	}
}

func TestMaybeSchedule_RunsSingleWorkerAtATime(t *testing.T) {
	m := New(Config{
		Enabled:   true,
		Directory: t.TempDir(),
		Interval:  time.Hour,
	})

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	m.runBackupFn = func(context.Context, time.Time) error {
		started <- struct{}{}
		<-release
		return nil
	}

	m.MaybeSchedule(context.Background())
	m.MaybeSchedule(context.Background())

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for backup worker to start")
	}

	select {
	case <-started:
		t.Fatal("second backup worker started while first was still running")
	default:
	}

	close(release)
	waitFor(t, 2*time.Second, func() bool { return !m.isRunning() })
}

func writeValidTestArtifact(t *testing.T, path string, createdAt time.Time, contents []byte) error {
	t.Helper()
	return writeValidTestArtifactFinalizedAt(t, path, createdAt, createdAt, contents)
}

func writeValidTestArtifactFinalizedAt(t *testing.T, path string, createdAt time.Time, finalizedAt time.Time, contents []byte) error {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return err
	}
	sum := sha256.Sum256(contents)
	meta := Metadata{
		FileName:    filepath.Base(path),
		CreatedAt:   createdAt,
		FinalizedAt: finalizedAt,
		SHA256:      hex.EncodeToString(sum[:]),
		SizeBytes:   int64(len(contents)),
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(metadataPath(path), data, 0o600)
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("condition was not met before timeout")
}
