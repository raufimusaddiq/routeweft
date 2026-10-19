package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/backup"
)

func TestBackupRoutesRequireSession(t *testing.T) {
	handler, _, store := newReadAPI(t)
	defer store.Close()
	mux := http.NewServeMux()
	handler.Attach(mux)
	for _, target := range []string{"/admin/v1/backup", "/admin/v1/backup/restore/check", "/admin/v1/backup/restore"} {
		recorder := httptest.NewRecorder()
		method := http.MethodGet
		if target != "/admin/v1/backup" {
			method = http.MethodPost
		}
		mux.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d want 401", target, recorder.Code)
		}
	}
}

func TestBackupDownloadAndRestoreCheck(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	handler.opts.DataDir = t.TempDir()
	var created string
	handler.opts.Backup = func(ctx context.Context, output string) (backup.Metadata, error) {
		meta, err := backup.Create(ctx, store, output)
		created = output
		return meta, err
	}
	handler.opts.Restore = func(context.Context, string) (backup.Metadata, error) {
		return backup.Metadata{SchemaVersion: 1, ConfigRevision: 1}, nil
	}
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: session.ID}

	download := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/backup", nil)
	request.AddCookie(cookie)
	mux.ServeHTTP(download, request)
	if download.Code != http.StatusOK || download.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("backup status=%d content-type=%s body=%s", download.Code, download.Header().Get("Content-Type"), download.Body.String())
	}
	if created == "" {
		t.Fatal("backup artifact was not created")
	}
	archive := download.Body.Bytes()
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("backup is not a valid zip: %v", err)
	}
	names := map[string]bool{}
	for _, file := range reader.File {
		names[file.Name] = true
	}
	if !names["routeweft.sqlite"] || !names["routeweft.sqlite.meta.json"] {
		t.Fatalf("archive entries=%v", names)
	}

	check := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/admin/v1/backup/restore/check", bytes.NewReader(archive))
	request.AddCookie(cookie)
	mux.ServeHTTP(check, request)
	if check.Code != http.StatusOK {
		t.Fatalf("restore check status=%d body=%s", check.Code, check.Body.String())
	}
	var payload struct {
		Valid bool `json:"valid"`
	}
	if err := json.Unmarshal(check.Body.Bytes(), &payload); err != nil || !payload.Valid {
		t.Fatalf("restore check payload=%s err=%v", check.Body.String(), err)
	}

	apply := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/admin/v1/backup/restore", bytes.NewReader(archive))
	request.AddCookie(cookie)
	mux.ServeHTTP(apply, request)
	if apply.Code != http.StatusAccepted {
		t.Fatalf("restore apply status=%d body=%s", apply.Code, apply.Body.String())
	}
	var applied struct {
		Accepted   bool   `json:"accepted"`
		Activation string `json:"activation"`
	}
	if err := json.Unmarshal(apply.Body.Bytes(), &applied); err != nil || !applied.Accepted || applied.Activation != "scheduled" {
		t.Fatalf("restore apply payload=%s err=%v", apply.Body.String(), err)
	}
}

func TestRestoreCheckRejectsForeignArchive(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	handler.opts.DataDir = t.TempDir()
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: session.ID}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("routeweft.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, "not a database"); err != nil {
		t.Fatal(err)
	}
	entry, err = writer.Create("routeweft.sqlite.meta.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, "{}"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/backup/restore/check", bytes.NewReader(archive.Bytes()))
	request.AddCookie(cookie)
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("foreign archive status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
