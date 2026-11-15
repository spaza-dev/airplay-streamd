package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestEmptyListsAreNonNil(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "indexer.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	jobs, err := db.ListJobs(ctx, "", "", 100, 0)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if jobs == nil {
		t.Fatal("ListJobs returned nil")
	}

	storage, err := db.StorageConfigs(ctx)
	if err != nil {
		t.Fatalf("list storage: %v", err)
	}
	if storage == nil {
		t.Fatal("StorageConfigs returned nil")
	}

	runs, err := db.DiscoveryRuns(ctx, 50)
	if err != nil {
		t.Fatalf("list discovery runs: %v", err)
	}
	if runs == nil {
		t.Fatal("DiscoveryRuns returned nil")
	}

	logs, err := db.JobLogs(ctx, 0, "", "", "", 100, 0)
	if err != nil {
		t.Fatalf("list job logs: %v", err)
	}
	if logs == nil {
		t.Fatal("JobLogs returned nil")
	}

	attempts, err := db.Attempts(ctx, 1)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if attempts == nil {
		t.Fatal("Attempts returned nil")
	}
}

func TestTimestampLists(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "indexer.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	db.AddJobLog(0, "info", "test", "test message")

	logs, err := db.JobLogs(ctx, 0, "", "", "", 100, 0)
	if err != nil {
		t.Fatalf("list job logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected one job log, got %d", len(logs))
	}
	if logs[0].CreatedAt.IsZero() {
		t.Fatal("job log created_at was not parsed")
	}

	if _, err := db.StartDiscoveryRun(ctx, "test"); err != nil {
		t.Fatalf("start discovery run: %v", err)
	}
	runs, err := db.DiscoveryRuns(ctx, 50)
	if err != nil {
		t.Fatalf("list discovery runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected one discovery run, got %d", len(runs))
	}
	if runs[0].StartedAt.IsZero() {
		t.Fatal("discovery run started_at was not parsed")
	}
}
