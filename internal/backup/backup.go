// Package backup owns validated SQLite backup artifacts and restore staging.
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/buildinfo"
	appruntime "github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

// Metadata identifies the database represented by a backup artifact.
type Metadata struct {
	SchemaVersion    int    `json:"schemaVersion"`
	RouteweftVersion string `json:"routeweftVersion"`
	RouteweftCommit  string `json:"routeweftCommit"`
	CreatedAt        string `json:"createdAt"`
	ConfigRevision   uint64 `json:"configRevision"`
}

// Candidate is a validated, migrated restore database in a private directory.
// The caller must either activate it or call Discard.
type Candidate struct {
	Path     string
	Dir      string
	Metadata Metadata
}

// Create writes an online-consistent, integrity-checked backup and metadata
// beside it. Neither existing destination is overwritten.
func Create(ctx context.Context, source *sqlite.Store, output string) (Metadata, error) {
	absolute, err := filepath.Abs(output)
	if err != nil {
		return Metadata{}, err
	}
	dir := filepath.Dir(absolute)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Metadata{}, fmt.Errorf("create backup directory: %w", err)
	}
	metaPath := absolute + ".meta.json"
	for _, path := range []string{absolute, metaPath} {
		if _, err := os.Lstat(path); err == nil {
			return Metadata{}, fmt.Errorf("backup destination already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return Metadata{}, fmt.Errorf("check backup destination: %w", err)
		}
	}
	stageDir, err := os.MkdirTemp(dir, ".routeweft-backup-*")
	if err != nil {
		return Metadata{}, fmt.Errorf("stage backup: %w", err)
	}
	defer os.RemoveAll(stageDir)
	stageDB := filepath.Join(stageDir, "routeweft.sqlite")
	if err := source.Backup(ctx, stageDB); err != nil {
		return Metadata{}, err
	}
	staged, err := sqlite.Open(ctx, stageDB)
	if err != nil {
		return Metadata{}, err
	}
	if err := staged.IntegrityCheck(ctx); err != nil {
		_ = staged.Close()
		return Metadata{}, err
	}
	version, err := migrations.NewRunner(staged.DB()).Version(ctx)
	if err != nil {
		_ = staged.Close()
		return Metadata{}, err
	}
	manager, err := appruntime.NewManager(ctx, staged.DB())
	if err != nil {
		_ = staged.Close()
		return Metadata{}, err
	}
	snapshot, err := manager.Load()
	if err != nil {
		_ = staged.Close()
		return Metadata{}, err
	}
	info := buildinfo.Current()
	meta := Metadata{SchemaVersion: version, RouteweftVersion: info.Version, RouteweftCommit: info.Commit, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ConfigRevision: snapshot.ConfigRevision()}
	if err := staged.CloseBackup(ctx); err != nil {
		return Metadata{}, err
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return Metadata{}, err
	}
	stageMeta := filepath.Join(stageDir, "routeweft.sqlite.meta.json")
	if err := writeSync(stageMeta, append(metaBytes, '\n')); err != nil {
		return Metadata{}, err
	}
	if err := os.Link(stageDB, absolute); err != nil {
		return Metadata{}, fmt.Errorf("publish backup database: %w", err)
	}
	if err := os.Link(stageMeta, metaPath); err != nil {
		_ = os.Remove(absolute)
		return Metadata{}, fmt.Errorf("publish backup metadata: %w", err)
	}
	if err := syncDir(dir); err != nil {
		_ = os.Remove(metaPath)
		_ = os.Remove(absolute)
		return Metadata{}, err
	}
	return meta, nil
}

// StageRestore validates and migrates a copied candidate without touching live
// state. Metadata, when present, must describe the original candidate.
func StageRestore(ctx context.Context, input, dataDir string) (*Candidate, error) {
	source, err := os.Open(input)
	if err != nil {
		return nil, fmt.Errorf("open restore candidate: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("restore candidate must be a regular file")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create restore staging directory: %w", err)
	}
	dir, err := os.MkdirTemp(dataDir, ".routeweft-restore-*")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "candidate.sqlite")
	if err := copySync(source, path); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("stage restore candidate: %w", err)
	}
	candidate, err := sqlite.Open(ctx, path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	fail := func(err error) (*Candidate, error) {
		_ = candidate.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := candidate.IntegrityCheck(ctx); err != nil {
		return fail(err)
	}
	runner := migrations.NewRunner(candidate.DB())
	hasVersions, err := runner.HasVersionTable(ctx)
	if err != nil {
		return fail(err)
	}
	if !hasVersions {
		return fail(fmt.Errorf("candidate %s is not a Routeweft database", input))
	}
	version, err := runner.Version(ctx)
	if err != nil {
		return fail(err)
	}
	if version > migrations.LatestVersion() {
		return fail(fmt.Errorf("candidate schema version %d is newer than supported %d", version, migrations.LatestVersion()))
	}
	if err := runner.Apply(ctx); err != nil {
		return fail(fmt.Errorf("migrate candidate: %w", err))
	}
	manager, err := appruntime.NewManager(ctx, candidate.DB())
	if err != nil {
		return fail(fmt.Errorf("compile candidate snapshot: %w", err))
	}
	snapshot, err := manager.Load()
	if err != nil {
		return fail(err)
	}
	meta, err := readMetadata(input + ".meta.json")
	if err != nil {
		return fail(err)
	}
	if meta != nil && (meta.SchemaVersion != version || meta.ConfigRevision != snapshot.ConfigRevision()) {
		return fail(errors.New("backup metadata does not match the database schema/configuration"))
	}
	if err := candidate.IntegrityCheck(ctx); err != nil {
		return fail(err)
	}
	if meta == nil {
		meta = &Metadata{SchemaVersion: version, ConfigRevision: snapshot.ConfigRevision()}
	}
	if err := candidate.CloseBackup(ctx); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &Candidate{Path: path, Dir: dir, Metadata: *meta}, nil
}

// Discard removes a candidate that was not activated.
func (c *Candidate) Discard() {
	if c != nil && c.Dir != "" {
		_ = os.RemoveAll(c.Dir)
	}
}

func readMetadata(path string) (*Metadata, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open backup metadata: %w", err)
	}
	defer file.Close()
	var meta Metadata
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	if err := decoder.Decode(&meta); err != nil {
		return nil, fmt.Errorf("decode backup metadata: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("backup metadata contains trailing data")
	}
	if meta.RouteweftVersion == "" || meta.RouteweftCommit == "" {
		return nil, errors.New("backup metadata is missing Routeweft version identity")
	}
	if _, err := time.Parse(time.RFC3339Nano, meta.CreatedAt); err != nil {
		return nil, fmt.Errorf("invalid backup metadata timestamp: %w", err)
	}
	return &meta, nil
}

func copySync(source io.Reader, path string) error {
	target, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(target, source)
	if copyErr == nil {
		copyErr = target.Sync()
	}
	closeErr := target.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func writeSync(path string, data []byte) error { return copySync(bytes.NewReader(data), path) }

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	err = dir.Sync()
	_ = dir.Close()
	if err != nil {
		return fmt.Errorf("sync backup directory: %w", err)
	}
	return nil
}
