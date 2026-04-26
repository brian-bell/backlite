package backup

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"modernc.org/sqlite"
)

const (
	artifactPrefix    = "backlite-"
	artifactExtension = ".sqlite.gz"
	metadataExtension = ".meta.json"
	timestampLayout   = "20060102T150405Z"
	backupStepPages   = 128
)

type Config struct {
	Enabled      bool
	DatabasePath string
	Directory    string
	Interval     time.Duration
}

type Metadata struct {
	FileName    string    `json:"file_name"`
	CreatedAt   time.Time `json:"created_at"`
	FinalizedAt time.Time `json:"finalized_at"`
	SHA256      string    `json:"sha256"`
	SizeBytes   int64     `json:"size_bytes"`
}

type Artifact struct {
	Path         string
	MetadataPath string
	Timestamp    time.Time
	Metadata     Metadata
}

type Manager struct {
	cfg Config

	mu      sync.Mutex
	running bool

	now         func() time.Time
	runBackupFn func(context.Context, time.Time) error
}

func New(cfg Config) *Manager {
	m := &Manager{
		cfg: cfg,
		now: time.Now,
	}
	m.runBackupFn = m.runBackup
	return m
}

// MaybeSchedule starts a single background backup worker when local backups are
// enabled and the latest finalized artifact is older than the configured
// interval. The call is non-blocking; backup work runs in a goroutine.
func (m *Manager) MaybeSchedule(ctx context.Context) {
	if !m.cfg.Enabled {
		return
	}
	if m.isRunning() {
		return
	}

	due, latest, err := m.needsBackup()
	if err != nil {
		log.Error().
			Err(err).
			Str("backup_dir", m.cfg.Directory).
			Msg("failed to inspect local backup state")
		return
	}
	if !due {
		return
	}

	startedAt := m.now().UTC().Truncate(time.Second)

	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()

	go func() {
		defer m.setRunning(false)

		logger := log.Info().
			Str("backup_dir", m.cfg.Directory).
			Str("database_path", m.cfg.DatabasePath)
		if latest != nil {
			logger = logger.Time("previous_backup_at", latest.Timestamp)
		}
		logger.Time("scheduled_at", startedAt).Msg("starting local sqlite backup")

		if err := m.runBackupFn(ctx, startedAt); err != nil {
			log.Error().
				Err(err).
				Str("backup_dir", m.cfg.Directory).
				Str("database_path", m.cfg.DatabasePath).
				Msg("local sqlite backup failed")
			return
		}

		log.Info().
			Str("backup_dir", m.cfg.Directory).
			Str("database_path", m.cfg.DatabasePath).
			Time("backup_at", startedAt).
			Msg("local sqlite backup completed")
	}()
}

func (m *Manager) isRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *Manager) setRunning(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = v
}

func (m *Manager) needsBackup() (bool, *Artifact, error) {
	latest, err := m.findLatestValidArtifact()
	if err != nil {
		return false, nil, err
	}
	if latest == nil {
		return true, nil, nil
	}
	return m.now().UTC().Sub(latest.ageReference()) >= m.cfg.Interval, latest, nil
}

// ageReference is the timestamp the scheduler uses to decide whether an
// artifact is older than the configured interval. Prefer the finalization
// time so a backup that takes longer than the interval does not immediately
// look stale on the next tick.
func (a *Artifact) ageReference() time.Time {
	if !a.Metadata.FinalizedAt.IsZero() {
		return a.Metadata.FinalizedAt
	}
	return a.Timestamp
}

func (m *Manager) findLatestValidArtifact() (*Artifact, error) {
	entries, err := os.ReadDir(m.cfg.Directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read backup directory: %w", err)
	}

	type candidate struct {
		path     string
		metaPath string
		ts       time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ts, ok := parseArtifactTimestamp(entry.Name())
		if !ok {
			continue
		}
		artifactPath := filepath.Join(m.cfg.Directory, entry.Name())
		candidates = append(candidates, candidate{
			path:     artifactPath,
			metaPath: metadataPath(artifactPath),
			ts:       ts,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ts.After(candidates[j].ts)
	})

	for _, c := range candidates {
		meta, valid, err := readMetadata(c.path, c.metaPath)
		if err != nil {
			return nil, err
		}
		if !valid {
			continue
		}
		if err := verifyArtifactChecksum(c.path, meta.SHA256); err != nil {
			log.Warn().
				Err(err).
				Str("artifact", c.path).
				Msg("backup artifact failed checksum verification; skipping")
			continue
		}
		return &Artifact{
			Path:         c.path,
			MetadataPath: c.metaPath,
			Timestamp:    c.ts,
			Metadata:     meta,
		}, nil
	}

	return nil, nil
}

func verifyArtifactChecksum(path string, expected string) error {
	if expected == "" {
		return fmt.Errorf("missing expected sha256")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open artifact for checksum verification: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash artifact for verification: %w", err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return fmt.Errorf("sha256 mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

func (m *Manager) runBackup(ctx context.Context, startedAt time.Time) error {
	if err := os.MkdirAll(m.cfg.Directory, 0o755); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}

	finalPath, rawTmpPath, gzipTmpPath, metadataTmpPath, err := m.preparePaths(startedAt)
	if err != nil {
		return err
	}

	verifyTmpPath := gzipTmpPath + ".verify"
	for _, path := range []string{rawTmpPath, gzipTmpPath, metadataTmpPath, verifyTmpPath} {
		if err := removeIfExists(path); err != nil {
			return err
		}
	}
	defer func() {
		for _, path := range []string{rawTmpPath, gzipTmpPath, metadataTmpPath, verifyTmpPath} {
			_ = os.Remove(path)
		}
	}()

	if err := onlineBackup(ctx, m.cfg.DatabasePath, rawTmpPath); err != nil {
		return err
	}
	if err := compressFile(rawTmpPath, gzipTmpPath); err != nil {
		return err
	}
	if err := validateCompressedSQLiteBackup(ctx, gzipTmpPath, verifyTmpPath); err != nil {
		return err
	}

	meta, err := describeArtifact(gzipTmpPath, filepath.Base(finalPath), startedAt)
	if err != nil {
		return err
	}

	if err := os.Rename(gzipTmpPath, finalPath); err != nil {
		return fmt.Errorf("finalize backup artifact: %w", err)
	}
	meta.FinalizedAt = m.now().UTC().Truncate(time.Second)
	if err := writeMetadata(metadataTmpPath, metadataPath(finalPath), meta); err != nil {
		_ = os.Remove(finalPath)
		return err
	}

	return nil
}

func (m *Manager) preparePaths(startedAt time.Time) (string, string, string, string, error) {
	timestamp := startedAt.UTC()

	for i := 0; i < 120; i++ {
		baseName := artifactPrefix + timestamp.Format(timestampLayout) + artifactExtension
		finalPath := filepath.Join(m.cfg.Directory, baseName)
		if _, err := os.Stat(finalPath); err == nil {
			timestamp = timestamp.Add(time.Second)
			continue
		} else if !os.IsNotExist(err) {
			return "", "", "", "", fmt.Errorf("inspect backup artifact path: %w", err)
		}

		rawTmpPath := finalPath + ".sqlite.tmp"
		gzipTmpPath := finalPath + ".tmp"
		metadataTmpPath := metadataPath(finalPath) + ".tmp"
		return finalPath, rawTmpPath, gzipTmpPath, metadataTmpPath, nil
	}

	return "", "", "", "", fmt.Errorf("failed to allocate unique backup artifact path")
}

func onlineBackup(ctx context.Context, sourcePath string, destinationPath string) error {
	if _, err := os.Stat(sourcePath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("source sqlite database does not exist: %s", sourcePath)
		}
		return fmt.Errorf("stat source sqlite database: %w", err)
	}

	db, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		return fmt.Errorf("open sqlite source database: %w", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("set backup busy timeout: %w", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire sqlite source connection: %w", err)
	}
	defer conn.Close()

	return conn.Raw(func(driverConn any) error {
		rawConn, ok := driverConn.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("sqlite driver does not expose online backup support")
		}

		backup, err := rawConn.NewBackup(destinationPath)
		if err != nil {
			return fmt.Errorf("initialize sqlite online backup: %w", err)
		}

		finished := false
		defer func() {
			if !finished {
				_ = backup.Finish()
				_ = os.Remove(destinationPath)
			}
		}()

		for {
			if err := ctx.Err(); err != nil {
				return err
			}

			more, err := backup.Step(backupStepPages)
			if err != nil {
				return fmt.Errorf("step sqlite online backup: %w", err)
			}
			if !more {
				break
			}
		}

		if err := backup.Finish(); err != nil {
			return fmt.Errorf("finish sqlite online backup: %w", err)
		}
		finished = true
		return nil
	})
}

func compressFile(sourcePath string, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open raw backup for compression: %w", err)
	}
	defer source.Close()

	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create compressed backup artifact: %w", err)
	}
	defer destination.Close()

	gz := gzip.NewWriter(destination)
	if _, err := io.Copy(gz, source); err != nil {
		gz.Close()
		return fmt.Errorf("compress backup artifact: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("close gzip writer: %w", err)
	}
	if err := destination.Close(); err != nil {
		return fmt.Errorf("close compressed backup artifact: %w", err)
	}

	return nil
}

func validateCompressedSQLiteBackup(ctx context.Context, compressedPath string, verifyPath string) error {
	if err := decompressFile(compressedPath, verifyPath); err != nil {
		return err
	}
	if err := validateSQLiteDatabase(ctx, verifyPath); err != nil {
		return err
	}
	return nil
}

func decompressFile(sourcePath string, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open compressed backup artifact: %w", err)
	}
	defer source.Close()

	reader, err := gzip.NewReader(source)
	if err != nil {
		return fmt.Errorf("open gzip reader for backup artifact: %w", err)
	}
	defer reader.Close()

	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create verification sqlite file: %w", err)
	}
	defer destination.Close()

	if _, err := io.Copy(destination, reader); err != nil {
		return fmt.Errorf("decompress backup artifact: %w", err)
	}
	if err := destination.Close(); err != nil {
		return fmt.Errorf("close verification sqlite file: %w", err)
	}

	return nil
}

func validateSQLiteDatabase(ctx context.Context, databasePath string) error {
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return fmt.Errorf("open verification sqlite database: %w", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("run sqlite integrity_check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("sqlite integrity_check failed: %s", result)
	}
	return nil
}

func describeArtifact(path string, fileName string, createdAt time.Time) (Metadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("open compressed artifact for metadata: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return Metadata{}, fmt.Errorf("hash compressed artifact: %w", err)
	}

	return Metadata{
		FileName:  fileName,
		CreatedAt: createdAt.UTC(),
		SHA256:    hex.EncodeToString(hash.Sum(nil)),
		SizeBytes: size,
	}, nil
}

func writeMetadata(tempPath string, finalPath string, meta Metadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal backup metadata: %w", err)
	}

	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create backup metadata temp file: %w", err)
	}

	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return fmt.Errorf("write backup metadata: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close backup metadata temp file: %w", err)
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return fmt.Errorf("finalize backup metadata: %w", err)
	}

	return nil
}

func metadataPath(artifactPath string) string {
	return artifactPath + metadataExtension
}

func parseArtifactTimestamp(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, artifactPrefix) || !strings.HasSuffix(name, artifactExtension) {
		return time.Time{}, false
	}

	ts := strings.TrimSuffix(strings.TrimPrefix(name, artifactPrefix), artifactExtension)
	parsed, err := time.Parse(timestampLayout, ts)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func readMetadata(artifactPath string, metadataPath string) (Metadata, bool, error) {
	stat, err := os.Stat(artifactPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Metadata{}, false, nil
		}
		return Metadata{}, false, fmt.Errorf("stat backup artifact: %w", err)
	}

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Metadata{}, false, nil
		}
		return Metadata{}, false, fmt.Errorf("read backup metadata: %w", err)
	}

	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return Metadata{}, false, nil
	}
	if meta.FileName != filepath.Base(artifactPath) {
		return Metadata{}, false, nil
	}
	if meta.SHA256 == "" || meta.SizeBytes <= 0 {
		return Metadata{}, false, nil
	}
	if stat.Size() != meta.SizeBytes {
		return Metadata{}, false, nil
	}

	return meta, true, nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove temp file %s: %w", path, err)
	}
	return nil
}
