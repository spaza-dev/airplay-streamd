package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	errCancelled = errors.New("job cancelled")
	errPaused    = errors.New("job paused")
)

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func permanent(err error) error { return permanentError{err: err} }

func isPermanent(err error) bool {
	var target permanentError
	return errors.As(err, &target)
}

type JobManager struct {
	app     *App
	kick    chan struct{}
	active  int32
	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
}

func NewJobManager(app *App) *JobManager {
	return &JobManager{
		app:     app,
		kick:    make(chan struct{}, 1),
		cancels: map[int64]context.CancelFunc{},
	}
}

func (m *JobManager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *JobManager) Active() int { return int(atomic.LoadInt32(&m.active)) }

func (m *JobManager) Cancel(id int64) {
	m.mu.Lock()
	cancel := m.cancels[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *JobManager) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		m.dispatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-m.kick:
		case <-ticker.C:
		}
	}
}

func (m *JobManager) dispatch(ctx context.Context) {
	for {
		if m.Active() >= m.app.cfg.Workers.MaxActiveJobs {
			return
		}
		job, err := m.app.db.ClaimNextJob(ctx)
		if err != nil {
			m.app.logger.Errorf("claim job: %v", err)
			return
		}
		if job == nil {
			return
		}
		atomic.AddInt32(&m.active, 1)
		go func(job *Job) {
			defer atomic.AddInt32(&m.active, -1)
			m.process(ctx, job)
		}(job)
	}
}

func (m *JobManager) process(parent context.Context, job *Job) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(m.app.cfg.Workers.JobTimeoutMinutes)*time.Minute)
	defer cancel()
	m.mu.Lock()
	m.cancels[job.ID] = cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.cancels, job.ID)
		m.mu.Unlock()
	}()

	err := m.execute(ctx, job)
	current, getErr := m.app.db.GetJob(context.Background(), job.ID)
	if getErr == nil {
		job = current
	}
	cancelFlag, pauseFlag := false, false
	if getErr == nil {
		cancelFlag = job.CancelRequested
		pauseFlag = job.PauseRequested
	}

	switch {
	case err == nil:
		m.app.db.FinishJob(context.Background(), job.ID, "completed", "")
		m.app.logJob(job.ID, "info", "complete", "job completed with %d matches", job.TotalMatches)
		if final, err := m.app.db.GetJob(context.Background(), job.ID); err == nil {
			m.app.deliverCallback(context.Background(), final, "job.completed", map[string]any{
				"total_indexes": final.TotalIndexes, "total_matches": final.TotalMatches,
			})
		}
		m.app.cleanupJob(job.ID)
	case cancelFlag:
		m.app.db.FinishJob(context.Background(), job.ID, "cancelled", "cancelled")
		if final, getErr := m.app.db.GetJob(context.Background(), job.ID); getErr == nil {
			m.app.deliverCallback(context.Background(), final, "job.cancelled", nil)
		}
		m.app.logJob(job.ID, "warn", "cancelled", "job cancelled")
		m.app.cleanupJob(job.ID)
	case errors.Is(err, errPaused) || pauseFlag:
		m.app.db.PauseJob(context.Background(), job.ID)
		if final, getErr := m.app.db.GetJob(context.Background(), job.ID); getErr == nil {
			m.app.deliverCallback(context.Background(), final, "job.paused", nil)
		}
		m.app.logJob(job.ID, "info", "paused", "job paused")
	case errors.Is(err, errCancelled):
		_ = m.app.db.ResetIndexesForRetry(context.Background(), job.ID)
		_ = m.app.db.ScheduleRetry(context.Background(), job.ID, "interrupted by shutdown", 0)
		m.app.logJob(job.ID, "warn", "interrupted", "job interrupted; it will resume after restart")
	case isPermanent(err):
		m.app.db.FinishJob(context.Background(), job.ID, "failed", err.Error())
		if final, getErr := m.app.db.GetJob(context.Background(), job.ID); getErr == nil {
			m.app.deliverCallback(context.Background(), final, "job.failed", map[string]any{"error": err.Error()})
		}
		m.app.logJob(job.ID, "error", "failed", "%v", err)
		m.app.cleanupJob(job.ID)
	default:
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("job timed out: %w", err)
		}
		if job.Attempts >= m.app.cfg.Workers.MaxAttempts {
			m.app.db.FinishJob(context.Background(), job.ID, "failed", err.Error())
			if final, getErr := m.app.db.GetJob(context.Background(), job.ID); getErr == nil {
				m.app.deliverCallback(context.Background(), final, "job.failed", map[string]any{"error": err.Error()})
			}
			m.app.logJob(job.ID, "error", "failed", "%v", err)
			m.app.cleanupJob(job.ID)
			return
		}
		_ = m.app.db.ResetIndexesForRetry(context.Background(), job.ID)
		delay := time.Duration(m.app.cfg.Workers.RetryInitialSecs) * time.Second
		for i := 1; i < job.Attempts; i++ {
			delay *= 2
			if delay >= time.Duration(m.app.cfg.Workers.RetryMaxSecs)*time.Second {
				delay = time.Duration(m.app.cfg.Workers.RetryMaxSecs) * time.Second
				break
			}
		}
		m.app.db.ScheduleRetry(context.Background(), job.ID, err.Error(), delay)
		m.app.logJob(job.ID, "warn", "retry", "attempt %d failed, retrying in %s: %v", job.Attempts, delay, err)
	}
}

func (m *JobManager) execute(ctx context.Context, job *Job) error {
	a := m.app
	workspace := a.workspacePath(job.ID)
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		return err
	}
	_ = a.db.SetJobStage(ctx, job.ID, "planning", "planning", 0.02)
	if job.Attempts == 1 {
		a.deliverCallback(ctx, job, "job.started", map[string]any{"total_files": job.TotalFiles})
		a.logJob(job.ID, "info", "start", "job started with %d files", job.TotalFiles)
	}

	files, err := a.db.JobFiles(ctx, job.ID)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return permanent(fmt.Errorf("job has no uploaded files"))
	}
	for _, file := range files {
		if file.Path == "" || !fileExists(file.Path) {
			return permanent(fmt.Errorf("uploaded file %q is missing", file.OriginalName))
		}
	}

	if _, err := a.planIndexes(ctx, job); err != nil {
		return err
	}
	indexes, err := a.db.JobIndexes(ctx, job.ID)
	if err != nil {
		return err
	}
	if len(indexes) == 0 {
		_ = a.db.SetJobTotals(ctx, job.ID, 0, 0, 0, 1)
		a.logJob(job.ID, "info", "planning", "no completed indexes matched the scope")
		return nil
	}

	var plannedBytes int64
	for _, idx := range indexes {
		plannedBytes += idx.SizeBytes
	}
	maxBytes := int64(a.cfg.Limits.MaxDownloadGB) * 1024 * 1024 * 1024
	if plannedBytes > maxBytes {
		return permanent(fmt.Errorf("indexes require %d bytes, exceeding the %d byte limit", plannedBytes, maxBytes))
	}
	if free, _ := diskFree(workspace); free < int64(a.cfg.Limits.MinFreeDiskGB)*1024*1024*1024 && free != 0 {
		return fmt.Errorf("free disk space %d bytes is below the configured minimum", free)
	}

	// Download indexes that are not already local.
	var pending []JobIndex
	for _, idx := range indexes {
		if idx.Status != "done" && idx.Status != "skipped" {
			pending = append(pending, idx)
		}
	}
	if len(pending) > 0 {
		_ = a.db.SetJobStage(ctx, job.ID, "downloading", "download", 0.1)
		var downloaded int64
		var downloadedMu sync.Mutex
		total := int64(len(pending))
		err := runParallel(ctx, a.cfg.Workers.DownloadsPerJob, len(pending), func(ctx context.Context, i int) error {
			idx := pending[i]
			if err := a.checkInterrupt(ctx, job.ID); err != nil {
				return err
			}
			if err := a.downloadIndex(ctx, job.ID, &idx); err != nil {
				_ = a.db.SetIndexStatus(context.Background(), idx.ID, "failed", err.Error(), idx.Matches)
				return fmt.Errorf("%s %s: %w", idx.StreamID, idx.Date, err)
			}
			downloadedMu.Lock()
			downloaded++
			progress := 0.1 + 0.2*(float64(downloaded)/float64(total))
			downloadedMu.Unlock()
			_ = a.db.SetJobStage(context.Background(), job.ID, "downloading", "download", progress)
			return nil
		})
		if err != nil {
			return err
		}
	}

	// Query each downloaded index and emit progressive callbacks.
	_ = a.db.SetJobStage(ctx, job.ID, "querying", "query", 0.3)
	totalIndexes := len(indexes)
	queryFiles := make([]string, 0, len(files))
	for _, file := range files {
		queryFiles = append(queryFiles, file.Path)
	}
	for i := range indexes {
		idx := indexes[i]
		if err := a.checkInterrupt(ctx, job.ID); err != nil {
			return err
		}
		if idx.Status == "done" || idx.Status == "skipped" {
			continue
		}
		if idx.Status != "downloaded" {
			if err := a.downloadIndex(ctx, job.ID, &idx); err != nil {
				return err
			}
		}
		if err := a.db.SetIndexStatus(ctx, idx.ID, "querying", "", idx.Matches); err != nil {
			return err
		}
		a.logJob(job.ID, "info", "query", "querying %s %s (%s)", idx.StreamID, idx.Date, idx.BuildID)
		indexDir := a.indexDir(job.ID, idx.ID)
		matches, output, err := RunOlafQuery(ctx, a.cfg,
			filepath.Join(indexDir, "home"), filepath.Join(indexDir, "db"),
			filepath.Join(indexDir, "cache"), indexDir, queryFiles)
		if err != nil {
			_ = a.db.SetIndexStatus(context.Background(), idx.ID, "failed", err.Error(), idx.Matches)
			a.logJob(job.ID, "error", "query", "%s %s: %v", idx.StreamID, idx.Date, err)
			if strings.TrimSpace(output) != "" {
				a.logJob(job.ID, "debug", "query", "olaf output: %s", truncate(output, 4000))
			}
			return err
		}
		timeline, err := readTimeline(filepath.Join(indexDir, "timeline.json"))
		if err != nil {
			_ = a.db.SetIndexStatus(context.Background(), idx.ID, "failed", err.Error(), idx.Matches)
			return err
		}
		scope, err := ParseScope(job.ScopeJSON)
		if err != nil {
			return permanent(err)
		}
		mapped := a.mapMatches(job, &idx, timeline, matches, files, scope)
		for _, match := range mapped {
			if err := a.db.AddMatch(ctx, &match); err != nil {
				return err
			}
		}
		_ = a.db.SetIndexStatus(ctx, idx.ID, "done", "", len(mapped))
		done, totalMatches, _ := a.db.IndexProgress(ctx, job.ID)
		progress := 0.3
		if totalIndexes > 0 {
			progress = 0.3 + 0.65*(float64(done)/float64(totalIndexes))
		}
		_ = a.db.SetJobTotals(ctx, job.ID, totalIndexes, done, totalMatches, progress)
		if len(mapped) > 0 {
			a.deliverCallback(ctx, job, "job.matches", map[string]any{
				"index": map[string]any{
					"id": idx.ID, "stream_id": idx.StreamID, "date": idx.Date,
					"build_id": idx.BuildID, "matches": len(mapped),
				},
				"matches": mapped,
			})
		}
		a.deliverCallback(ctx, job, "job.progress", map[string]any{
			"processed_indexes": done, "total_indexes": totalIndexes,
			"total_matches": totalMatches, "progress": progress,
		})
		a.logJob(job.ID, "info", "query", "%s %s: %d matches", idx.StreamID, idx.Date, len(mapped))
	}
	return nil
}

func (a *App) checkInterrupt(ctx context.Context, jobID int64) error {
	cancel, pause, err := a.db.JobFlags(ctx, jobID)
	if err == nil {
		if cancel {
			return errCancelled
		}
		if pause {
			return errPaused
		}
	}
	select {
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			return ctx.Err()
		}
		return errCancelled
	default:
		return nil
	}
}

func (a *App) downloadIndex(ctx context.Context, jobID int64, idx *JobIndex) error {
	indexDir := a.indexDir(jobID, idx.ID)
	dataPath := filepath.Join(indexDir, "db", "data.mdb")
	timelinePath := filepath.Join(indexDir, "timeline.json")
	if fileExists(dataPath) && fileExists(timelinePath) {
		_ = a.db.SetIndexStatus(ctx, idx.ID, "downloaded", "", idx.Matches)
		idx.Status = "downloaded"
		return nil
	}
	_ = a.db.SetIndexStatus(ctx, idx.ID, "downloading", "", idx.Matches)
	if _, err := a.storage.DownloadToFile(ctx, idx.DataKey, dataPath); err != nil {
		return fmt.Errorf("download data.mdb: %w", err)
	}
	if a.storage.Exists(ctx, idx.LockKey) {
		if _, err := a.storage.DownloadToFile(ctx, idx.LockKey, filepath.Join(indexDir, "db", "lock.mdb")); err != nil {
			return fmt.Errorf("download lock.mdb: %w", err)
		}
	}
	if _, err := a.storage.DownloadToFile(ctx, idx.TimelineKey, timelinePath); err != nil {
		return fmt.Errorf("download timeline.json: %w", err)
	}
	if _, err := readTimeline(timelinePath); err != nil {
		return fmt.Errorf("validate timeline.json: %w", err)
	}
	_ = a.db.SetIndexStatus(ctx, idx.ID, "downloaded", "", idx.Matches)
	idx.Status = "downloaded"
	a.logJob(jobID, "info", "download", "%s %s index downloaded", idx.StreamID, idx.Date)
	return nil
}

func (a *App) mapMatches(job *Job, idx *JobIndex, timeline *Timeline, matches []OlafMatch, files []JobFile, scope *Scope) []Match {
	byResource := map[string]Recording{}
	for _, recording := range timeline.Recordings {
		if recording.ResourceID != "" {
			byResource[recording.ResourceID] = recording
		}
		if recording.OlafReferenceID != "" {
			byResource[recording.OlafReferenceID] = recording
		}
		if recording.ObjectKey != "" {
			byResource[recording.ObjectKey] = recording
		}
	}
	fileByName := map[string]JobFile{}
	for _, file := range files {
		fileByName[file.StoredName] = file
		fileByName[filepath.Base(file.Path)] = file
		fileByName[file.OriginalName] = file
	}
	timezone := timeline.Timezone
	if timezone == "" {
		timezone = idx.Timezone
	}
	if timezone == "" {
		timezone = a.cfg.DefaultTimezone
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.UTC
		timezone = "UTC"
	}
	out := make([]Match, 0, len(matches))
	for _, item := range matches {
		recording, ok := byResource[item.Path]
		if !ok {
			continue
		}
		refStart := item.ReferenceStart
		refStop := item.ReferenceStop
		if refStop < refStart {
			refStop = refStart
		}
		refDuration := time.Duration((refStop - refStart) * float64(time.Second))
		occurrence := recording.StartTime.Add(time.Duration(refStart * float64(time.Second))).UTC()
		if !scope.AllowsOccurrence(occurrence, refDuration, timezone) {
			continue
		}
		file := fileByName[item.QueryPath]
		if file.ID == 0 {
			file = fileByName[filepath.Base(item.QueryPath)]
		}
		fileName := file.OriginalName
		if fileName == "" {
			fileName = filepath.Base(item.QueryPath)
		}
		startMS := recording.TimelineStartMS + int64(refStart*1000)
		endMS := startMS + int64((refStop-refStart)*1000)
		if startMS < recording.TimelineStartMS {
			startMS = recording.TimelineStartMS
		}
		if recording.TimelineEndMS > 0 && endMS > recording.TimelineEndMS {
			endMS = recording.TimelineEndMS
		}
		if endMS < startMS {
			endMS = startMS
		}
		out = append(out, Match{
			JobID: job.ID, JobIndexID: idx.ID, FileID: file.ID, FileName: fileName,
			QueryPath: item.QueryPath, ResourceID: recording.ResourceID,
			StreamID: idx.StreamID, Date: occurrence.In(loc).Format("2006-01-02"),
			RecordingStart: recording.StartTime.UTC(), Occurrence: occurrence,
			ReferenceStartMS: int64(refStart * 1000), ReferenceStopMS: int64(refStop * 1000),
			MatchCount: item.MatchCount, Score: item.Score,
			TimelineStartMS: startMS, TimelineEndMS: endMS,
		})
	}
	return out
}

func (a *App) cleanupJob(jobID int64) {
	_ = a.db.ClearFilePaths(context.Background(), jobID)
	workspace := a.workspacePath(jobID)
	if err := os.RemoveAll(workspace); err != nil {
		a.logJob(jobID, "warn", "cleanup", "remove workspace: %v", err)
	}
}

func (a *App) indexDir(jobID, indexID int64) string {
	return filepath.Join(a.workspacePath(jobID), "indexes", fmt.Sprintf("%d", indexID))
}

func (a *App) workspacePath(jobID int64) string {
	return filepath.Join(a.cfg.Database.Workspace, fmt.Sprintf("%d", jobID))
}

func readTimeline(path string) (*Timeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var timeline Timeline
	if err := json.Unmarshal(data, &timeline); err != nil {
		return nil, err
	}
	return &timeline, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func diskFree(path string) (int64, int64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	return int64(stat.Bavail) * int64(stat.Bsize), int64(stat.Blocks) * int64(stat.Bsize)
}

func runParallel(ctx context.Context, concurrency, count int, fn func(context.Context, int) error) error {
	if concurrency <= 0 {
		concurrency = 1
	}
	if count == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	items := make(chan int)
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	workers := concurrency
	if workers > count {
		workers = count
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range items {
				if ctx.Err() != nil {
					return
				}
				if err := fn(ctx, index); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					errMu.Unlock()
					return
				}
			}
		}()
	}
sendLoop:
	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			break sendLoop
		case items <- i:
		}
	}
	close(items)
	wg.Wait()
	return firstErr
}

func (a *App) retentionLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	a.pruneRetention(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.pruneRetention(ctx)
		}
	}
}

func (a *App) pruneRetention(ctx context.Context) {
	if a.cfg.Retention.JobsDays > 0 {
		before := time.Now().UTC().AddDate(0, 0, -a.cfg.Retention.JobsDays)
		ids, err := a.db.ExpiredJobIDs(ctx, before)
		if err != nil {
			a.logger.Warnf("retention: %v", err)
		}
		for _, id := range ids {
			_ = os.RemoveAll(a.workspacePath(id))
		}
		if err := a.db.DeleteJobs(ctx, ids); err != nil {
			a.logger.Warnf("retention delete jobs: %v", err)
		}
		if len(ids) > 0 {
			a.logger.Infof("retention removed %d expired jobs", len(ids))
		}
	}
	if a.cfg.Retention.LogsDays > 0 {
		before := time.Now().UTC().AddDate(0, 0, -a.cfg.Retention.LogsDays)
		if err := a.db.PruneLogs(ctx, before); err != nil {
			a.logger.Warnf("retention logs: %v", err)
		}
	}
}
