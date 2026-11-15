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
		result := ProcessResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1}
		return result, ctx.Err()
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
	for _, args := range [][]string{{"--version"}, {"help"}} {
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

func writeOlafConfig(home, dbDir, cacheDir string) error {
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
		"skip_duplicates":               true,
		"allowed_audio_file_extensions": supportedExtensionList(),
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(configDir, "olaf_config.json")
	return os.WriteFile(path, data, 0o640)
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

func RunOlafCache(ctx context.Context, cfg *Config, workspace string, recordings []Recording, paths map[string]string) error {
	home := filepath.Join(workspace, "olaf-home")
	argsPrefix := []string{"cache", "--threads", strconv.Itoa(cfg.Olaf.Threads), "--with-ids"}
	var batch []string
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		timeout := time.Duration(cfg.Olaf.TimeoutMinutes) * time.Minute
		runCtx, cancel := commandTimeout(ctx, timeout)
		defer cancel()
		args := append(append([]string{}, argsPrefix...), batch...)
		result, err := runCommand(runCtx, cfg.Olaf.Binary, args, workspace, olafEnv(home))
		if err != nil {
			return fmt.Errorf("olaf cache failed (exit %d): %w: %s", result.ExitCode, err, strings.TrimSpace(result.Stderr))
		}
		batch = nil
		return nil
	}
	for _, recording := range recordings {
		if recording.Status == "failed" {
			continue
		}
		path := paths[recording.ObjectKey]
		if path == "" {
			return fmt.Errorf("missing local path for %s", recording.ObjectKey)
		}
		batch = append(batch, path, recording.OlafReferenceID)
		if len(batch) >= 512 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func RunOlafStoreCached(ctx context.Context, cfg *Config, workspace string) error {
	home := filepath.Join(workspace, "olaf-home")
	timeout := time.Duration(cfg.Olaf.TimeoutMinutes) * time.Minute
	runCtx, cancel := commandTimeout(ctx, timeout)
	defer cancel()
	result, err := runCommand(runCtx, cfg.Olaf.Binary, []string{"store_cached"}, workspace, olafEnv(home))
	if err != nil {
		return fmt.Errorf("olaf store_cached failed (exit %d): %w: %s", result.ExitCode, err, strings.TrimSpace(result.Stderr))
	}
	return nil
}

var songCountPattern = regexp.MustCompile(`Number of songs[^:]*:\s*(\d+)`)
var fingerprintCountPattern = regexp.MustCompile(`Number of items in databases:\s*(\d+)`)

func RunOlafStats(ctx context.Context, cfg *Config, workspace string) (int, string, error) {
	home := filepath.Join(workspace, "olaf-home")
	runCtx, cancel := commandTimeout(ctx, 2*time.Minute)
	defer cancel()
	result, err := runCommand(runCtx, cfg.Olaf.Binary, []string{"stats", "--verbose"}, workspace, olafEnv(home))
	output := result.Stdout + result.Stderr
	if err != nil {
		return 0, output, fmt.Errorf("olaf stats failed (exit %d): %w", result.ExitCode, err)
	}
	match := songCountPattern.FindStringSubmatch(output)
	if match == nil {
		return 0, output, fmt.Errorf("could not parse Olaf database statistics")
	}
	count, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, output, err
	}
	return count, output, nil
}

func OlafFingerprintCount(output string) (int, error) {
	match := fingerprintCountPattern.FindStringSubmatch(output)
	if match == nil {
		return 0, fmt.Errorf("could not parse Olaf fingerprint count")
	}
	return strconv.Atoi(match[1])
}

type ProbeResult struct {
	DurationMS int64
	Format     string
	Codec      string
	SampleRate int
	Channels   int
}

type ffprobeOutput struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecName  string `json:"codec_name"`
		SampleRate string `json:"sample_rate"`
		Channels   int    `json:"channels"`
	} `json:"streams"`
}

func ProbeAudio(ctx context.Context, cfg *Config, path string) (ProbeResult, error) {
	runCtx, cancel := commandTimeout(ctx, time.Duration(cfg.FFprobe.TimeoutSeconds)*time.Second)
	defer cancel()
	args := []string{
		"-v", "error",
		"-show_entries", "format=format_name,duration:stream=codec_name,sample_rate,channels",
		"-of", "json",
		path,
	}
	result, err := runCommand(runCtx, cfg.FFprobe.Binary, args, "", nil)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("ffprobe failed: %w: %s", err, strings.TrimSpace(result.Stderr))
	}
	var output ffprobeOutput
	if err := json.Unmarshal([]byte(result.Stdout), &output); err != nil {
		return ProbeResult{}, fmt.Errorf("parse ffprobe output: %w", err)
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(output.Format.Duration), 64)
	if err != nil || seconds <= 0 {
		return ProbeResult{}, fmt.Errorf("ffprobe returned invalid duration %q", output.Format.Duration)
	}
	probe := ProbeResult{
		DurationMS: int64(seconds*1000 + 0.5),
		Format:     output.Format.FormatName,
	}
	if len(output.Streams) > 0 {
		stream := output.Streams[0]
		probe.Codec = stream.CodecName
		probe.SampleRate, _ = strconv.Atoi(stream.SampleRate)
		probe.Channels = stream.Channels
	}
	return probe, nil
}

func ProbeURL(ctx context.Context, cfg *Config, url string) (ProbeResult, error) {
	runCtx, cancel := commandTimeout(ctx, time.Duration(cfg.FFprobe.TimeoutSeconds)*time.Second)
	defer cancel()
	args := []string{
		"-v", "error",
		"-show_entries", "format=format_name,duration:stream=codec_name,sample_rate,channels",
		"-of", "json",
		url,
	}
	result, err := runCommand(runCtx, cfg.FFprobe.Binary, args, "", nil)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("ffprobe failed for remote object: %w: %s", err, strings.TrimSpace(result.Stderr))
	}
	var output ffprobeOutput
	if err := json.Unmarshal([]byte(result.Stdout), &output); err != nil {
		return ProbeResult{}, err
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(output.Format.Duration), 64)
	if err != nil || seconds <= 0 {
		return ProbeResult{}, fmt.Errorf("ffprobe returned invalid duration %q", output.Format.Duration)
	}
	return ProbeResult{DurationMS: int64(seconds*1000 + 0.5), Format: output.Format.FormatName}, nil
}

func ValidateIndex(workspace string, expectedResources int) (int, error) {
	if expectedResources <= 0 {
		return 0, fmt.Errorf("timeline contains no resources")
	}
	dataPath := filepath.Join(workspace, "index", "data.mdb")
	info, err := os.Stat(dataPath)
	if err != nil {
		return 0, fmt.Errorf("LMDB data file missing: %w", err)
	}
	if info.Size() == 0 {
		return 0, fmt.Errorf("LMDB data file is empty")
	}
	return int(info.Size()), nil
}
