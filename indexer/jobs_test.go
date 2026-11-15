package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildArtifactPaths(t *testing.T) {
	dest := &StorageClient{cfg: StorageConfig{Prefix: "/archive/"}}
	basePrefix := "capital-fm-ke/indexes/2026/11/14/"
	buildID := "20261115T115743Z-3"

	got := buildArtifactPaths(dest, basePrefix, buildID)
	want := map[string]string{
		"meta":     "archive/capital-fm-ke/indexes/2026/11/14/meta.json",
		"date":     "archive/capital-fm-ke/indexes/2026/11/14/",
		"build":    "archive/capital-fm-ke/indexes/2026/11/14/builds/20261115T115743Z-3/",
		"data":     "archive/capital-fm-ke/indexes/2026/11/14/builds/20261115T115743Z-3/data.mdb",
		"lock":     "archive/capital-fm-ke/indexes/2026/11/14/builds/20261115T115743Z-3/lock.mdb",
		"timeline": "archive/capital-fm-ke/indexes/2026/11/14/builds/20261115T115743Z-3/timeline.json",
	}
	for name, expected := range want {
		if got[name] != expected {
			t.Errorf("path %q = %q, want %q", name, got[name], expected)
		}
	}
}

func TestBuildCompletionMetaIncludesArtifactPaths(t *testing.T) {
	job := &Job{
		StreamID:  "capital-fm-ke",
		Date:      "2026-11-14",
		CreatedAt: time.Date(2026, 11, 15, 2, 30, 0, 0, time.UTC),
	}
	timeline := &Timeline{
		Version:    1,
		Recordings: []Recording{{}, {}},
		Summary: TimelineSummary{
			RecordingsDiscovered: 2,
			RecordingsIndexed:    2,
		},
	}
	paths := map[string]string{
		"timeline": "archive/capital-fm-ke/indexes/2026/11/14/builds/build-1/timeline.json",
	}
	meta := buildCompletionMeta(completionMetaInput{
		Job:              job,
		Timezone:         "Africa/Nairobi",
		BuildID:          "build-1",
		Timeline:         timeline,
		TimelineChecksum: "sha256:abc",
		TimelineSize:     123,
		IndexSizes:       map[string]int64{"data.mdb": 456, "lock.mdb": 78},
		Paths:            paths,
		OlafVersion:      "v3.3.3",
		OlafThreads:      4,
		CompletedAt:      time.Date(2026, 11, 15, 2, 42, 0, 0, time.UTC),
	})

	if meta["meta_version"] != 1 {
		t.Fatalf("meta_version = %v, want 1", meta["meta_version"])
	}
	if meta["build_prefix"] != "builds/build-1/" {
		t.Fatalf("build_prefix = %v", meta["build_prefix"])
	}
	gotPaths, ok := meta["paths"].(map[string]string)
	if !ok || gotPaths["timeline"] != paths["timeline"] {
		t.Fatalf("paths = %#v", meta["paths"])
	}
	timelineMeta, ok := meta["timeline"].(map[string]any)
	if !ok || timelineMeta["size_bytes"] != int64(123) {
		t.Fatalf("timeline metadata = %#v", meta["timeline"])
	}
	indexMeta, ok := meta["index"].(map[string]any)
	if !ok {
		t.Fatalf("index metadata = %#v", meta["index"])
	}
	sizes, ok := indexMeta["sizes_bytes"].(map[string]int64)
	if !ok || sizes["data.mdb"] != 456 {
		t.Fatalf("index sizes = %#v", indexMeta["sizes_bytes"])
	}
}

func TestFinishJobCompletedSetsFullProgress(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "indexer.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	job, _, err := db.CreateJob(ctx, "test-stream", "2026-11-14", "test", false)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	claimed, err := db.ClaimNextJob(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim job: %v", err)
	}
	if err := db.UpdateJob(ctx, claimed.ID, "uploading", "upload", 0.9, ""); err != nil {
		t.Fatalf("update job: %v", err)
	}
	if err := db.FinishJob(ctx, claimed.ID, "completed", ""); err != nil {
		t.Fatalf("finish job: %v", err)
	}
	got, err := db.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Progress != 1 {
		t.Fatalf("progress = %v, want 1", got.Progress)
	}
	if got.Stage != "completed" {
		t.Fatalf("stage = %q, want completed", got.Stage)
	}
	if got.CompletedAt == nil {
		t.Fatal("completed_at is nil")
	}
}

func TestJobStagesReturnsStageTransitions(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "indexer.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	job, _, err := db.CreateJob(ctx, "test-stream", "2026-11-14", "test", false)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	db.AddJobLog(job.ID, "info", "download", "stage=download progress=0%")
	db.AddJobLog(job.ID, "info", "validate", "stream=test-stream date=2026-11-14")
	db.AddJobLog(job.ID, "info", "cache", "stage=cache progress=40%")
	stages, err := db.JobStages(ctx, job.ID)
	if err != nil {
		t.Fatalf("job stages: %v", err)
	}
	if len(stages) != 2 {
		t.Fatalf("stages = %d, want 2", len(stages))
	}
	if stages[0].Stage != "download" || stages[1].Stage != "cache" {
		t.Fatalf("unexpected stage order: %#v", stages)
	}
}
