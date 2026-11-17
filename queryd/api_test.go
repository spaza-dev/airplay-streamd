package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleCreateJobAcceptsFileURLs(t *testing.T) {
	app := newDownloadTestApp(t, true)
	app.storage = &StorageClient{}
	app.jobs = NewJobManager(app)

	body := `{
		"files": [
			"https://example.com/clips/clip-1.wav",
			"https://example.com/clips/clip-2.mp3?token=abc"
		],
		"scope": {"streams": {"mode": "all"}, "dates": {"mode": "date", "date": "2026-01-02"}},
		"callback_url": "https://example.com/hooks/queryd"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.handleCreateJob(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Job Job `json:"job"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Job.TotalFiles != 2 {
		t.Fatalf("total files = %d", response.Job.TotalFiles)
	}
	files, err := app.db.JobFiles(context.Background(), response.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d", len(files))
	}
	if files[0].OriginalName != "clip-1.wav" || files[0].SourceURL != "https://example.com/clips/clip-1.wav" {
		t.Fatalf("unexpected first file: %+v", files[0])
	}
	if files[1].OriginalName != "clip-2.mp3" || files[1].SourceURL != "https://example.com/clips/clip-2.mp3?token=abc" {
		t.Fatalf("unexpected second file: %+v", files[1])
	}
	if files[0].Path != "" || files[1].Path != "" {
		t.Fatalf("URL files should not have local paths before download: %+v", files)
	}
}

func TestHandleCreateJobRejectsMultipart(t *testing.T) {
	app := newDownloadTestApp(t, true)
	app.storage = &StorageClient{}
	app.jobs = NewJobManager(app)

	req := httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewBufferString("not multipart"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	rec := httptest.NewRecorder()
	app.handleCreateJob(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCreateJobRejectsInvalidFileURL(t *testing.T) {
	app := newDownloadTestApp(t, true)
	app.storage = &StorageClient{}
	app.jobs = NewJobManager(app)

	body := `{
		"files": ["ftp://example.com/clip.wav"],
		"scope": {"streams": {"mode": "all"}, "dates": {"mode": "date", "date": "2026-01-02"}}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.handleCreateJob(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
