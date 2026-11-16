package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type ProcessResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func runCommand(ctx context.Context, name string, args []string, dir string, env []string) (ProcessResult, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return ProcessResult{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		result := ProcessResult{Stdout: stdout.String(), Stderr: stderr.String()}
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				result.ExitCode = exitErr.ExitCode()
			} else {
				result.ExitCode = -1
			}
		}
		return result, err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return ProcessResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1}, ctx.Err()
	}
}

func commandTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

var olafVersionPattern = regexp.MustCompile(`Olaf\s+([0-9]{4}\.[0-9]{2}\.[0-9]{2})`)

func DetectOlafVersion(ctx context.Context, binary string) (string, error) {
	for _, args := range [][]string{{"help"}, {"--version"}} {
		runCtx, cancel := commandTimeout(ctx, 10*time.Second)
		result, _ := runCommand(runCtx, binary, args, "", nil)
		cancel()
		match := olafVersionPattern.FindStringSubmatch(result.Stdout + result.Stderr)
		if match != nil {
			return match[1], nil
		}
	}
	return "", fmt.Errorf("could not determine Olaf version from %s", binary)
}

func supportedExtensions() []string {
	return []string{".mp3", ".wav", ".flac", ".ogg", ".opus", ".m4a", ".aac", ".wma", ".aiff", ".aif"}
}

func supportedExtension(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, supported := range supportedExtensions() {
		if ext == supported {
			return true
		}
	}
	return false
}

func writeOlafConfig(home, dbDir, cacheDir string, cfg *Config) error {
	configDir := filepath.Join(home, ".olaf")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(dbDir, 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return err
	}
	doc := map[string]any{
		"db_folder":                     strings.TrimRight(dbDir, "/") + "/",
		"cache_folder":                  cacheDir,
		"target_sample_rate":            cfg.Olaf.TargetSampleRate,
		"min_match_count":               cfg.Olaf.MinMatchCount,
		"max_results":                   cfg.Olaf.MaxResults,
		"skip_duplicates":               true,
		"allowed_audio_file_extensions": supportedExtensions(),
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(configDir, "olaf_config.json"), data, 0o640)
}

func olafEnv(home string) []string {
	out := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "HOME=") {
			continue
		}
		out = append(out, item)
	}
	return append(out, "HOME="+home)
}

type OlafMatch struct {
	QueryPath      string
	Path           string
	MatchCount     int
	ReferenceStart float64
	ReferenceStop  float64
	Score          float64
}

func RunOlafQuery(ctx context.Context, cfg *Config, home, dbDir, cacheDir, workDir string, files []string) ([]OlafMatch, string, error) {
	if err := writeOlafConfig(home, dbDir, cacheDir, cfg); err != nil {
		return nil, "", err
	}
	args := []string{"query", "--threads", strconv.Itoa(cfg.Workers.OlafThreads), "--format", "json"}
	args = append(args, files...)
	runCtx, cancel := commandTimeout(ctx, time.Duration(cfg.Olaf.TimeoutMinutes)*time.Minute)
	defer cancel()
	result, err := runCommand(runCtx, cfg.Olaf.Binary, args, workDir, olafEnv(home))
	output := result.Stdout
	if strings.TrimSpace(output) == "" {
		output = result.Stderr
	}
	if err != nil {
		return nil, output, fmt.Errorf("olaf query failed (exit %d): %w: %s",
			result.ExitCode, err, strings.TrimSpace(result.Stderr))
	}
	matches, err := parseOlafOutput([]byte(result.Stdout))
	if err != nil {
		return nil, output, err
	}
	return matches, output, nil
}

func parseOlafOutput(data []byte) ([]OlafMatch, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("olaf produced no JSON output")
	}
	var matches []OlafMatch
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	decodedAny := false
	for {
		var doc any
		err := decoder.Decode(&doc)
		if err != nil {
			break
		}
		decodedAny = true
		collectOlafMatches(doc, "", &matches)
	}
	if !decodedAny {
		var doc any
		if err := json.Unmarshal(trimmed, &doc); err != nil {
			return nil, fmt.Errorf("parse olaf JSON: %w", err)
		}
		collectOlafMatches(doc, "", &matches)
	}
	return matches, nil
}

func collectOlafMatches(node any, inheritedQuery string, out *[]OlafMatch) {
	switch value := node.(type) {
	case []any:
		for _, item := range value {
			collectOlafMatches(item, inheritedQuery, out)
		}
	case map[string]any:
		query := inheritedQuery
		for _, key := range []string{"query_path", "query", "audio_file", "filename"} {
			if text, ok := value[key].(string); ok && text != "" {
				query = text
				break
			}
		}
		if list, ok := value["matches"].([]any); ok {
			for _, item := range list {
				if matchMap, ok := item.(map[string]any); ok {
					if match, ok := olafMatchFromMap(matchMap, query); ok {
						*out = append(*out, match)
					}
				}
			}
			return
		}
		if match, ok := olafMatchFromMap(value, query); ok {
			*out = append(*out, match)
			return
		}
		for key, child := range value {
			if key == "matches" {
				continue
			}
			collectOlafMatches(child, query, out)
		}
	}
}

func olafMatchFromMap(value map[string]any, query string) (OlafMatch, bool) {
	path := ""
	for _, key := range []string{"path", "resource_id", "resource", "matched_path"} {
		if text, ok := value[key].(string); ok && text != "" {
			path = text
			break
		}
	}
	if path == "" {
		return OlafMatch{}, false
	}
	if query == "" {
		if text, ok := value["query_path"].(string); ok {
			query = text
		}
	}
	start, hasStart := numberField(value, "reference_start", "reference_start_seconds", "start")
	if !hasStart {
		return OlafMatch{}, false
	}
	stop, _ := numberField(value, "reference_stop", "reference_stop_seconds", "stop")
	count := int(numberFieldDefault(value, "match_count", "matches_count", "count"))
	score := numberFieldDefault(value, "score", "confidence")
	return OlafMatch{
		QueryPath:      query,
		Path:           path,
		MatchCount:     count,
		ReferenceStart: start,
		ReferenceStop:  stop,
		Score:          score,
	}, true
}

func numberField(value map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		raw, ok := value[key]
		if !ok {
			continue
		}
		switch number := raw.(type) {
		case json.Number:
			parsed, err := number.Float64()
			if err == nil {
				return parsed, true
			}
		case float64:
			return number, true
		case int:
			return float64(number), true
		case string:
			parsed, err := strconv.ParseFloat(number, 64)
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func numberFieldDefault(value map[string]any, keys ...string) float64 {
	number, _ := numberField(value, keys...)
	return number
}
