package api

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/backup"
)

const maxRestoreArchiveBytes = 8 << 30

func (h *Handler) handleBackup(w http.ResponseWriter, r *http.Request) {
	if h.opts.Backup == nil || h.opts.DataDir == "" {
		writeError(w, http.StatusServiceUnavailable, "backup_unavailable", "backup is not configured")
		return
	}
	dir, err := os.MkdirTemp(h.opts.DataDir, ".routeweft-admin-backup-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_failed", "could not stage database backup")
		return
	}
	defer os.RemoveAll(dir)
	database := filepath.Join(dir, "routeweft.sqlite")
	meta, err := h.opts.Backup(r.Context(), database)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_failed", "database backup failed")
		return
	}
	archivePath := filepath.Join(dir, "routeweft-backup.zip")
	archive, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_failed", "could not package database backup")
		return
	}
	zipWriter := zip.NewWriter(archive)
	if err := addZipFile(zipWriter, "routeweft.sqlite", database); err == nil {
		err = addZipFile(zipWriter, "routeweft.sqlite.meta.json", database+".meta.json")
	}
	if err == nil {
		err = zipWriter.Close()
	} else {
		_ = zipWriter.Close()
	}
	if err == nil {
		err = archive.Sync()
	}
	closeErr := archive.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_failed", "could not package database backup")
		return
	}
	file, err := os.Open(archivePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_failed", "could not read database backup")
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_failed", "could not read database backup")
		return
	}
	filename := "routeweft-" + time.Now().UTC().Format("20060102T150405Z") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Routeweft-Config-Revision", fmt.Sprint(meta.ConfigRevision))
	http.ServeContent(w, r, filename, stat.ModTime(), file)
}

func addZipFile(writer *zip.Writer, name, path string) error {
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0o600)
	target, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(target, source)
	return err
}

func (h *Handler) handleRestoreCheck(w http.ResponseWriter, r *http.Request) {
	dir, dbPath, err := h.extractRestoreArchive(w, r)
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	candidate, err := backup.StageRestore(r.Context(), dbPath, h.opts.DataDir)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_backup", "backup failed integrity, schema, metadata, or configuration validation")
		return
	}
	defer candidate.Discard()
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "schemaVersion": candidate.Metadata.SchemaVersion, "configRevision": candidate.Metadata.ConfigRevision})
}

func (h *Handler) handleRestore(w http.ResponseWriter, r *http.Request) {
	if h.opts.Restore == nil {
		writeError(w, http.StatusServiceUnavailable, "restore_unavailable", "restore is not configured")
		return
	}
	dir, dbPath, err := h.extractRestoreArchive(w, r)
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	meta, err := h.opts.Restore(r.Context(), dbPath)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		writeError(w, http.StatusBadRequest, "restore_rejected", "restore candidate could not be staged")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "activation": "scheduled", "schemaVersion": meta.SchemaVersion, "configRevision": meta.ConfigRevision})
}

func (h *Handler) extractRestoreArchive(w http.ResponseWriter, r *http.Request) (string, string, error) {
	if h.opts.DataDir == "" {
		writeError(w, http.StatusServiceUnavailable, "restore_unavailable", "restore staging is not configured")
		return "", "", errors.New("restore staging is not configured")
	}
	if r.ContentLength > maxRestoreArchiveBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "backup_too_large", "backup archive exceeds the 8 GiB limit")
		return "", "", errors.New("backup archive too large")
	}
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "invalid_backup", "backup archive is required")
		return "", "", errors.New("missing body")
	}
	dir, err := os.MkdirTemp(h.opts.DataDir, ".routeweft-restore-upload-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "restore_failed", "could not stage backup upload")
		return "", "", err
	}
	archivePath := filepath.Join(dir, "upload.zip")
	target, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		writeError(w, http.StatusInternalServerError, "restore_failed", "could not stage backup upload")
		return "", "", err
	}
	limited := http.MaxBytesReader(w, r.Body, maxRestoreArchiveBytes)
	_, copyErr := io.Copy(target, limited)
	if copyErr == nil {
		copyErr = target.Sync()
	}
	closeErr := target.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.RemoveAll(dir)
		var maxErr *http.MaxBytesError
		if errors.As(copyErr, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "backup_too_large", "backup archive exceeds the 8 GiB limit")
			return "", "", copyErr
		}
		writeError(w, http.StatusBadRequest, "invalid_backup", "backup archive could not be read")
		return "", "", copyErr
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		_ = os.RemoveAll(dir)
		writeError(w, http.StatusBadRequest, "invalid_backup", "backup archive is not a valid Routeweft archive")
		return "", "", err
	}
	defer reader.Close()
	entries := map[string]*zip.File{}
	var uncompressedTotal uint64
	for _, entry := range reader.File {
		if entry.Name != "routeweft.sqlite" && entry.Name != "routeweft.sqlite.meta.json" {
			_ = os.RemoveAll(dir)
			writeError(w, http.StatusBadRequest, "invalid_backup", "backup archive contains an unexpected entry")
			return "", "", errors.New("unexpected archive entry")
		}
		uncompressedTotal += entry.UncompressedSize64
		if len(reader.File) > 2 || uncompressedTotal > maxRestoreArchiveBytes ||
			entry.UncompressedSize64 > maxRestoreArchiveBytes || entry.CompressedSize64 > maxRestoreArchiveBytes ||
			entries[entry.Name] != nil || !entry.Mode().IsRegular() {
			_ = os.RemoveAll(dir)
			writeError(w, http.StatusBadRequest, "invalid_backup", "backup archive has duplicate or invalid entries")
			return "", "", errors.New("invalid archive entry")
		}
		entries[entry.Name] = entry
	}
	if len(entries) != 2 {
		_ = os.RemoveAll(dir)
		writeError(w, http.StatusBadRequest, "invalid_backup", "backup archive must contain database and metadata")
		return "", "", errors.New("missing archive entry")
	}
	for _, name := range []string{"routeweft.sqlite", "routeweft.sqlite.meta.json"} {
		if err := extractZipFile(entries[name], filepath.Join(dir, name)); err != nil {
			_ = os.RemoveAll(dir)
			writeError(w, http.StatusBadRequest, "invalid_backup", "backup archive entry could not be extracted")
			return "", "", err
		}
	}
	return dir, filepath.Join(dir, "routeweft.sqlite"), nil
}

func extractZipFile(entry *zip.File, destination string) error {
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	copied, copyErr := io.Copy(target, io.LimitReader(source, maxRestoreArchiveBytes+1))
	if copyErr == nil {
		copyErr = target.Sync()
	}
	closeErr := target.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if copied > maxRestoreArchiveBytes || uint64(copied) != entry.UncompressedSize64 {
		return errors.New("archive entry too large")
	}
	return nil
}
