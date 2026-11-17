package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	fileDownloadTimeout = 10 * time.Minute
	maxURLRedirects     = 5
)

func (a *App) httpClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           a.safeDialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       30 * time.Second,
		},
		Timeout:       fileDownloadTimeout,
		CheckRedirect: a.checkRedirect,
	}
}

func (a *App) safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if a.cfg.Limits.AllowPrivateURLs {
		return dialer.DialContext(ctx, network, address)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, candidate := range ips {
		if isBlockedIP(candidate.IP) {
			continue
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
	}
	return nil, fmt.Errorf("destination %s resolves only to blocked addresses", host)
}

func (a *App) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxURLRedirects {
		return fmt.Errorf("too many redirects (limit %d)", maxURLRedirects)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
	}
	if req.URL.Host == "" {
		return fmt.Errorf("redirect to a relative URL is not allowed")
	}
	if req.URL.User != nil {
		return fmt.Errorf("redirect to a URL with credentials is not allowed")
	}
	return nil
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 0 || (v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127) {
			return true
		}
	}
	return false
}

func (a *App) downloadJobFiles(ctx context.Context, jobID int64, files []JobFile) ([]JobFile, error) {
	var pending []JobFile
	var total int64
	for _, file := range files {
		if file.Path != "" && fileExists(file.Path) {
			total += file.SizeBytes
			continue
		}
		pending = append(pending, file)
	}
	if len(pending) == 0 {
		return files, nil
	}
	maxTotal := int64(a.cfg.Limits.MaxTotalUploadMB) << 20
	_ = a.db.SetJobStage(ctx, jobID, "downloading", "fetch_files", 0.02)
	var mu sync.Mutex
	done := 0
	err := runParallel(ctx, a.cfg.Workers.DownloadsPerJob, len(pending), func(ctx context.Context, i int) error {
		if err := a.checkInterrupt(ctx, jobID); err != nil {
			return err
		}
		if err := a.downloadJobFile(ctx, jobID, &pending[i]); err != nil {
			return err
		}
		mu.Lock()
		total += pending[i].SizeBytes
		if total > maxTotal {
			mu.Unlock()
			return permanent(fmt.Errorf("downloaded files exceed the %d MB total limit", a.cfg.Limits.MaxTotalUploadMB))
		}
		done++
		progress := 0.02 + 0.08*(float64(done)/float64(len(pending)))
		mu.Unlock()
		_ = a.db.SetJobStage(context.Background(), jobID, "downloading", "fetch_files", progress)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return a.db.JobFiles(ctx, jobID)
}

func (a *App) downloadJobFile(ctx context.Context, jobID int64, file *JobFile) error {
	if strings.TrimSpace(file.SourceURL) == "" {
		return permanent(fmt.Errorf("file %q has no source URL", file.OriginalName))
	}
	if file.Path != "" && fileExists(file.Path) {
		return nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, fileDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, file.SourceURL, nil)
	if err != nil {
		return permanent(fmt.Errorf("download %s: %w", file.OriginalName, err))
	}
	req.Header.Set("User-Agent", "queryd/"+version)
	resp, err := a.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("download %s: %w", file.OriginalName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := fmt.Sprintf("download %s: HTTP %d", file.OriginalName, resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			return permanent(errors.New(message))
		}
		return errors.New(message)
	}
	maxBytes := int64(a.cfg.Limits.MaxUploadMB) << 20
	if resp.ContentLength > maxBytes {
		return permanent(fmt.Errorf("%s exceeds the %d MB file limit", file.OriginalName, a.cfg.Limits.MaxUploadMB))
	}
	ext, err := downloadExtension(file, resp)
	if err != nil {
		return permanent(err)
	}
	uploadDir := filepath.Join(a.workspacePath(jobID), "uploads")
	if err := os.MkdirAll(uploadDir, 0o750); err != nil {
		return err
	}
	stored := fmt.Sprintf("%d.%s", file.ID, ext)
	path := filepath.Join(uploadDir, stored)
	tmp := path + ".part"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(dst, io.LimitReader(resp.Body, maxBytes+1))
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if n > maxBytes {
		_ = os.Remove(tmp)
		return permanent(fmt.Errorf("%s exceeds the %d MB file limit", file.OriginalName, a.cfg.Limits.MaxUploadMB))
	}
	if n == 0 {
		_ = os.Remove(tmp)
		return permanent(fmt.Errorf("download %s returned an empty body", file.OriginalName))
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if err := a.db.SetJobFileDownloaded(ctx, file.ID, stored, path, contentType, n); err != nil {
		return err
	}
	file.StoredName = stored
	file.Path = path
	file.SizeBytes = n
	file.ContentType = contentType
	a.logJob(jobID, "info", "download", "%s downloaded from %s (%d bytes)", file.OriginalName, file.SourceURL, n)
	return nil
}

func downloadExtension(file *JobFile, resp *http.Response) (string, error) {
	candidates := make([]string, 0, 2)
	if parsed, err := url.Parse(file.SourceURL); err == nil {
		if name, err := url.PathUnescape(filepath.Base(parsed.Path)); err == nil {
			candidates = append(candidates, name)
		}
	}
	if disposition := resp.Header.Get("Content-Disposition"); disposition != "" {
		if _, params, err := mime.ParseMediaType(disposition); err == nil {
			candidates = append(candidates, params["filename"])
		}
	}
	for _, candidate := range candidates {
		if supportedExtension(candidate) {
			return strings.TrimPrefix(strings.ToLower(filepath.Ext(candidate)), "."), nil
		}
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	extensions := map[string]string{
		"audio/mpeg":     "mp3",
		"audio/mp3":      "mp3",
		"audio/wav":      "wav",
		"audio/wave":     "wav",
		"audio/x-wav":    "wav",
		"audio/flac":     "flac",
		"audio/x-flac":   "flac",
		"audio/ogg":      "ogg",
		"audio/opus":     "opus",
		"audio/mp4":      "m4a",
		"audio/x-m4a":    "m4a",
		"audio/aac":      "aac",
		"audio/aacp":     "aac",
		"audio/x-ms-wma": "wma",
		"audio/aiff":     "aiff",
		"audio/x-aiff":   "aiff",
	}
	if ext, ok := extensions[contentType]; ok {
		return ext, nil
	}
	return "", fmt.Errorf("unsupported audio type for %s (url path, content-disposition, and content-type %q)", file.OriginalName, contentType)
}
