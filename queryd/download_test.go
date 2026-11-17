package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newDownloadTestApp(t *testing.T, allowPrivate bool) *App {
	t.Helper()
	root := t.TempDir()
	db, err := OpenDB(filepath.Join(root, "queryd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &App{
		cfg: &Config{
			Database: DatabaseConfig{Workspace: filepath.Join(root, "jobs")},
			Limits: LimitsConfig{
				MaxUploadFiles: 10, MaxUploadMB: 1, MaxTotalUploadMB: 2,
				MinFreeDiskGB: 0, AllowPrivateURLs: allowPrivate,
			},
			Workers: WorkersConfig{DownloadsPerJob: 2},
		},
		db:     db,
		logger: &Logger{level: 1, max: 100},
	}
}

func addDownloadTestFile(t *testing.T, app *App, sourceURL string) (*Job, *JobFile) {
	t.Helper()
	ctx := context.Background()
	job, err := app.db.CreateJob(ctx, `{"streams":{"mode":"all"},"dates":{"mode":"date","date":"2026-01-02"}}`, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	file, err := app.db.AddJobFile(ctx, job.ID, "clip", "", "", "", "", sourceURL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return job, file
}

func TestDownloadJobFileUsesContentTypeExtension(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFF....WAVE"))
	}))
	defer server.Close()

	app := newDownloadTestApp(t, true)
	job, file := addDownloadTestFile(t, app, server.URL+"/clip-without-extension")
	if err := app.downloadJobFile(context.Background(), job.ID, file); err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(file.Path) != ".wav" {
		t.Fatalf("path = %q, want .wav", file.Path)
	}
	if file.SizeBytes != int64(len("RIFF....WAVE")) {
		t.Fatalf("size = %d", file.SizeBytes)
	}
	if file.ContentType != "audio/wav" {
		t.Fatalf("content type = %q", file.ContentType)
	}
	if _, err := os.Stat(file.Path); err != nil {
		t.Fatalf("downloaded file missing: %v", err)
	}
	files, err := app.db.JobFiles(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].StoredName != file.StoredName || files[0].Path != file.Path {
		t.Fatalf("unexpected stored files: %+v", files)
	}
}

func TestDownloadJobFileUsesContentDispositionExtension(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="clip.flac"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("fLaC"))
	}))
	defer server.Close()

	app := newDownloadTestApp(t, true)
	job, file := addDownloadTestFile(t, app, server.URL+"/download")
	if err := app.downloadJobFile(context.Background(), job.ID, file); err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(file.Path) != ".flac" {
		t.Fatalf("path = %q, want .flac", file.Path)
	}
}

func TestDownloadJobFileRejectsUnsupportedType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("not audio"))
	}))
	defer server.Close()

	app := newDownloadTestApp(t, true)
	job, file := addDownloadTestFile(t, app, server.URL+"/notes")
	err := app.downloadJobFile(context.Background(), job.ID, file)
	if err == nil || !isPermanent(err) {
		t.Fatalf("err = %v, want permanent error", err)
	}
}

func TestDownloadJobFileRejectsOversizedFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte(strings.Repeat("x", 1024*1024+1)))
	}))
	defer server.Close()

	app := newDownloadTestApp(t, true)
	job, file := addDownloadTestFile(t, app, server.URL+"/clip.mp3")
	err := app.downloadJobFile(context.Background(), job.ID, file)
	if err == nil || !isPermanent(err) {
		t.Fatalf("err = %v, want permanent error", err)
	}
}

func TestDownloadJobFileClassifiesHTTPErrors(t *testing.T) {
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer notFound.Close()
	app := newDownloadTestApp(t, true)
	job, file := addDownloadTestFile(t, app, notFound.URL+"/clip.mp3")
	if err := app.downloadJobFile(context.Background(), job.ID, file); err == nil || !isPermanent(err) {
		t.Fatalf("404 err = %v, want permanent error", err)
	}

	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "later", http.StatusServiceUnavailable)
	}))
	defer unavailable.Close()
	job, file = addDownloadTestFile(t, app, unavailable.URL+"/clip.mp3")
	if err := app.downloadJobFile(context.Background(), job.ID, file); err == nil || isPermanent(err) {
		t.Fatalf("503 err = %v, want retryable error", err)
	}
}

func TestDownloadJobFileBlocksPrivateAddresses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFF....WAVE"))
	}))
	defer server.Close()

	app := newDownloadTestApp(t, false)
	job, file := addDownloadTestFile(t, app, server.URL+"/clip.wav")
	if err := app.downloadJobFile(context.Background(), job.ID, file); err == nil {
		t.Fatal("expected private address to be blocked")
	}
}
