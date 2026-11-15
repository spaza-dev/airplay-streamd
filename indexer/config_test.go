package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoveryConfigValidation(t *testing.T) {
	cfg := defaultDiscoveryConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default discovery config should be valid: %v", err)
	}
	cfg.StreamScope = "selected"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected selected stream scope without streams to fail")
	}
	cfg.SelectedStreams = []string{"radio-one"}
	cfg.DateScope = "explicit"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected explicit date scope without dates to fail")
	}
	cfg.Dates = []string{"2026-11-14"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid explicit configuration rejected: %v", err)
	}
	cfg.Dates = []string{"14/11/2026"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid date to fail")
	}
}

func TestRuntimeSettingsValidation(t *testing.T) {
	settings := runtimeSettings{
		MaxActiveJobs: 1, RetryInitialSeconds: 60, RetryMaxSeconds: 3600,
		MaxAttempts: 3, KeepFailedWorkspaces: true, MinFreeDiskGB: 1, BuildsRetained: 3,
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	settings.RetryMaxSeconds = 30
	if err := settings.Validate(); err == nil {
		t.Fatal("expected retry maximum below initial to fail")
	}
	settings.RetryMaxSeconds = 3600
	settings.BuildsRetained = 0
	if err := settings.Validate(); err == nil {
		t.Fatal("expected zero retained builds to fail")
	}
}

func TestLoadConfigExpandsEnvironment(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("INDEXER_ADMIN_TOKEN=secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content := []byte(`
server:
  listen: "127.0.0.1:9999"
  admin_token: "${INDEXER_ADMIN_TOKEN}"
database:
  path: "` + filepath.Join(dir, "indexer.db") + `"
  workspace: "` + filepath.Join(dir, "jobs") + `"
logging:
  level: "debug"
  file: "` + filepath.Join(dir, "indexer.log") + `"
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(configPath, envPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Server.AdminToken != "secret-token" {
		t.Fatalf("admin token not expanded: %q", cfg.Server.AdminToken)
	}
	if cfg.Olaf.Binary != "/usr/local/bin/olaf" {
		t.Fatalf("defaults not applied: %q", cfg.Olaf.Binary)
	}
}
