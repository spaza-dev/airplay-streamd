package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type DiscoveryConfig struct {
	Enabled             bool            `json:"enabled"`
	ScanIntervalMinutes int             `json:"scan_interval_minutes"`
	SourceStorage       string          `json:"source_storage"`
	StreamScope         string          `json:"stream_scope"`
	SelectedStreams     []string        `json:"selected_streams"`
	DateScope           string          `json:"date_scope"`
	LookbackDays        int             `json:"lookback_days"`
	Dates               []string        `json:"dates,omitempty"`
	Readiness           ReadinessConfig `json:"readiness"`
	AutomaticEnqueue    bool            `json:"automatic_enqueue"`
	MaxJobsPerScan      int             `json:"max_jobs_per_scan"`
	DryRun              bool            `json:"dry_run"`
}

type ReadinessConfig struct {
	Strategy              string `json:"strategy"`
	GracePeriodMinutes    int    `json:"grace_period_minutes"`
	MinimumRecordings     int    `json:"minimum_recordings"`
	MinimumDurationSecond int    `json:"minimum_duration_seconds"`
}

func defaultDiscoveryConfig() DiscoveryConfig {
	return DiscoveryConfig{
		Enabled:             false,
		ScanIntervalMinutes: 30,
		SourceStorage:       "source",
		StreamScope:         "all",
		SelectedStreams:     []string{},
		DateScope:           "previous_days",
		LookbackDays:        7,
		Readiness: ReadinessConfig{
			Strategy:              "grace_period",
			GracePeriodMinutes:    30,
			MinimumRecordings:     1,
			MinimumDurationSecond: 0,
		},
		AutomaticEnqueue: true,
		MaxJobsPerScan:   100,
		DryRun:           false,
	}
}

func (c *DiscoveryConfig) Validate() error {
	if c.ScanIntervalMinutes <= 0 {
		return errors.New("scan_interval_minutes must be greater than zero")
	}
	if strings.TrimSpace(c.SourceStorage) == "" {
		return errors.New("source_storage is required")
	}
	if c.StreamScope != "all" && c.StreamScope != "selected" {
		return fmt.Errorf("stream_scope must be all or selected")
	}
	if c.StreamScope == "selected" && len(c.SelectedStreams) == 0 {
		return errors.New("selected_streams must not be empty when stream_scope is selected")
	}
	if c.DateScope != "previous_days" && c.DateScope != "explicit" {
		return fmt.Errorf("date_scope must be previous_days or explicit")
	}
	if c.DateScope == "previous_days" && c.LookbackDays <= 0 {
		return errors.New("lookback_days must be greater than zero")
	}
	if c.DateScope == "explicit" && len(c.Dates) == 0 {
		return errors.New("dates must not be empty when date_scope is explicit")
	}
	if c.Readiness.Strategy != "grace_period" && c.Readiness.Strategy != "completion_event" {
		return fmt.Errorf("readiness.strategy must be grace_period or completion_event")
	}
	if c.Readiness.GracePeriodMinutes < 0 {
		return errors.New("readiness.grace_period_minutes must not be negative")
	}
	if c.Readiness.MinimumRecordings < 0 {
		return errors.New("readiness.minimum_recordings must not be negative")
	}
	if c.Readiness.MinimumDurationSecond < 0 {
		return errors.New("readiness.minimum_duration_seconds must not be negative")
	}
	if c.MaxJobsPerScan <= 0 {
		return errors.New("max_jobs_per_scan must be greater than zero")
	}
	for _, stream := range c.SelectedStreams {
		if !streamIDPattern.MatchString(stream) {
			return fmt.Errorf("invalid selected stream %q", stream)
		}
	}
	for _, date := range c.Dates {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return fmt.Errorf("invalid date %q", date)
		}
	}
	return nil
}

type Discovery struct {
	app      *App
	mu       sync.Mutex
	lastScan time.Time
}

func NewDiscovery(app *App) *Discovery {
	return &Discovery{app: app}
}

func (d *Discovery) LoadConfig(ctx context.Context) (DiscoveryConfig, error) {
	cfg := defaultDiscoveryConfig()
	raw, enabled, err := d.app.db.DiscoveryConfig(ctx)
	if err != nil {
		return cfg, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return cfg, err
		}
	}
	cfg.Enabled = enabled
	return cfg, nil
}

func (d *Discovery) SaveConfig(ctx context.Context, cfg DiscoveryConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return d.app.db.SaveDiscoveryConfig(ctx, string(data), cfg.Enabled)
}

func (d *Discovery) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if d.app.draining.Load() {
			return
		}
		cfg, err := d.LoadConfig(ctx)
		if err == nil && cfg.Enabled {
			if time.Since(d.lastScan) >= time.Duration(cfg.ScanIntervalMinutes)*time.Minute {
				if _, err := d.RunNow(ctx, "scheduler"); err != nil {
					d.app.logger.Warnf("discovery scan: %v", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Discovery) RunNow(ctx context.Context, trigger string) (*DiscoveryRun, error) {
	if !d.mu.TryLock() {
		return nil, errors.New("a discovery scan is already running")
	}
	defer d.mu.Unlock()
	d.lastScan = time.Now()
	cfg, err := d.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	run, err := d.scan(ctx, cfg, trigger)
	if run != nil {
		run.Trigger = trigger
	}
	return run, err
}

func (d *Discovery) scan(ctx context.Context, cfg DiscoveryConfig, trigger string) (*DiscoveryRun, error) {
	if d.app.draining.Load() {
		return nil, errors.New("service is shutting down")
	}
	runID, err := d.app.db.StartDiscoveryRun(ctx, trigger)
	if err != nil {
		return nil, err
	}
	run := &DiscoveryRun{Trigger: trigger, Status: "completed"}
	var errorMessages []string

	sourceCfg, err := d.app.db.StorageConfigByName(ctx, cfg.SourceStorage)
	if err != nil {
		run.Status = "failed"
		errorMessages = append(errorMessages, "source storage: "+err.Error())
		run.Errors = strings.Join(errorMessages, "; ")
		_ = d.app.db.FinishDiscoveryRun(ctx, runID, *run)
		return run, err
	}
	source, err := NewStorageClient(ctx, *sourceCfg)
	if err != nil {
		run.Status = "failed"
		run.Errors = err.Error()
		_ = d.app.db.FinishDiscoveryRun(ctx, runID, *run)
		return run, err
	}
	destCfg, destErr := d.app.db.StorageConfigByRole(ctx, "destination")
	if destErr != nil {
		run.Status = "failed"
		run.Errors = "destination storage is not configured"
		_ = d.app.db.FinishDiscoveryRun(ctx, runID, *run)
		return run, fmt.Errorf("destination storage is not configured")
	}
	dest, err := NewStorageClient(ctx, *destCfg)
	if err != nil {
		run.Status = "failed"
		run.Errors = err.Error()
		_ = d.app.db.FinishDiscoveryRun(ctx, runID, *run)
		return run, err
	}

	streams, err := d.streamIDs(ctx, source, cfg)
	if err != nil {
		run.Status = "failed"
		run.Errors = err.Error()
		_ = d.app.db.FinishDiscoveryRun(ctx, runID, *run)
		return run, err
	}

	for _, streamID := range streams {
		if ctx.Err() != nil {
			break
		}
		run.StreamsExamined++
		tzName, loc, err := resolveStreamTimezone(ctx, d.app, source, streamID)
		if err != nil {
			errorMessages = append(errorMessages, fmt.Sprintf("%s: %v", streamID, err))
			continue
		}
		_ = tzName
		dates := d.scanDates(cfg, loc)
		for _, date := range dates {
			if ctx.Err() != nil {
				break
			}
			run.DatesExamined++
			eligible, reason, err := d.evaluateDate(ctx, source, streamID, date, cfg)
			if err != nil {
				errorMessages = append(errorMessages, fmt.Sprintf("%s/%s: %v", streamID, date, err))
				continue
			}
			if !eligible {
				if reason != "" {
					d.app.logger.Debugf("discovery skip %s/%s: %s", streamID, date, reason)
				}
				continue
			}
			run.EligibleDates++
			if d.completedIndexExists(ctx, dest, streamID, date) {
				run.JobsSkipped++
				continue
			}
			if _, err := d.app.db.findActiveJob(ctx, streamID, date); err == nil {
				run.JobsSkipped++
				continue
			}
			if !cfg.AutomaticEnqueue || cfg.DryRun {
				continue
			}
			if run.JobsCreated >= cfg.MaxJobsPerScan {
				run.JobsSkipped++
				continue
			}
			job, existing, err := d.app.db.CreateJob(ctx, streamID, date, trigger, false)
			if err != nil {
				errorMessages = append(errorMessages, fmt.Sprintf("%s/%s: %v", streamID, date, err))
				continue
			}
			if existing {
				run.JobsSkipped++
				continue
			}
			run.JobsCreated++
			d.app.logger.Infof("discovery created job %d for %s/%s", job.ID, streamID, date)
			d.app.jobs.Kick()
		}
	}

	if len(errorMessages) > 0 {
		run.Status = "completed_with_errors"
		run.Errors = strings.Join(errorMessages, "; ")
	}
	_ = d.app.db.FinishDiscoveryRun(ctx, runID, *run)
	return run, nil
}

func (d *Discovery) streamIDs(ctx context.Context, source *StorageClient, cfg DiscoveryConfig) ([]string, error) {
	if cfg.StreamScope == "selected" {
		return append([]string(nil), cfg.SelectedStreams...), nil
	}
	streams, err := source.ListCommonPrefixes(ctx, "")
	if err != nil {
		return nil, err
	}
	return streams, nil
}

func (d *Discovery) scanDates(cfg DiscoveryConfig, loc *time.Location) []string {
	if cfg.DateScope == "explicit" {
		return append([]string(nil), cfg.Dates...)
	}
	today := time.Now().In(loc)
	var dates []string
	for i := 1; i <= cfg.LookbackDays; i++ {
		dates = append(dates, today.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	return dates
}

func (d *Discovery) evaluateDate(ctx context.Context, source *StorageClient, streamID, date string, cfg DiscoveryConfig) (bool, string, error) {
	prefix := fmt.Sprintf("%s/recordings/%s/", streamID, strings.ReplaceAll(date, "-", "/"))
	objects, err := source.ListObjects(ctx, prefix)
	if err != nil {
		return false, "", err
	}
	var recordings []ObjectInfo
	for _, object := range objects {
		if supportedAudioExtensions[audioExtension(object.Key)] {
			recordings = append(recordings, object)
		}
	}
	if len(recordings) == 0 {
		return false, "no recordings", nil
	}
	if len(recordings) < cfg.Readiness.MinimumRecordings {
		return false, "minimum recordings not met", nil
	}
	switch cfg.Readiness.Strategy {
	case "completion_event":
		if !d.app.db.HasRecordingEvent(streamID, date) {
			return false, "no completion event", nil
		}
	case "grace_period":
		latest := recordings[0].LastModified
		for _, object := range recordings[1:] {
			if object.LastModified.After(latest) {
				latest = object.LastModified
			}
		}
		if time.Since(latest) < time.Duration(cfg.Readiness.GracePeriodMinutes)*time.Minute {
			return false, "grace period not elapsed", nil
		}
	}
	if cfg.Readiness.MinimumDurationSecond > 0 {
		total, err := d.totalDuration(ctx, source, recordings)
		if err != nil {
			return false, "duration check failed: " + err.Error(), nil
		}
		if total < int64(cfg.Readiness.MinimumDurationSecond*1000) {
			return false, "minimum duration not met", nil
		}
	}
	return true, "", nil
}

func (d *Discovery) totalDuration(ctx context.Context, source *StorageClient, objects []ObjectInfo) (int64, error) {
	var sum int64
	var mu sync.Mutex
	err := runParallel(ctx, 4, len(objects), func(ctx context.Context, index int) error {
		url, err := source.PresignGet(ctx, objects[index].Key, 15*time.Minute)
		if err != nil {
			return err
		}
		probe, err := ProbeURL(ctx, d.app.cfg, url)
		if err != nil {
			return err
		}
		mu.Lock()
		sum += probe.DurationMS
		mu.Unlock()
		return nil
	})
	return sum, err
}

func (d *Discovery) completedIndexExists(ctx context.Context, dest *StorageClient, streamID, date string) bool {
	key := fmt.Sprintf("%s/indexes/%s/meta.json", streamID, strings.ReplaceAll(date, "-", "/"))
	data, err := dest.GetBytes(ctx, key, 1<<20)
	if err != nil {
		return false
	}
	var meta struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	return meta.Status == "complete"
}

func (d *Discovery) HandleRecordingEvent(ctx context.Context, streamID, date string) (*Job, error) {
	if !streamIDPattern.MatchString(streamID) {
		return nil, fmt.Errorf("invalid stream id")
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return nil, fmt.Errorf("invalid date")
	}
	if !d.mu.TryLock() {
		return nil, errors.New("discovery scan is running; retry shortly")
	}
	defer d.mu.Unlock()

	cfg, err := d.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	sourceCfg, err := d.app.db.StorageConfigByName(ctx, cfg.SourceStorage)
	if err != nil {
		sourceCfg, err = d.app.db.StorageConfigByRole(ctx, "source")
		if err != nil {
			return nil, err
		}
	}
	source, err := NewStorageClient(ctx, *sourceCfg)
	if err != nil {
		return nil, err
	}
	prefix := fmt.Sprintf("%s/recordings/%s/", streamID, strings.ReplaceAll(date, "-", "/"))
	objects, err := source.ListObjects(ctx, prefix)
	if err != nil {
		return nil, err
	}
	count := 0
	for _, object := range objects {
		if supportedAudioExtensions[audioExtension(object.Key)] {
			count++
		}
	}
	if count == 0 {
		return nil, fmt.Errorf("no recordings found for %s/%s", streamID, date)
	}
	job, existing, err := d.app.db.CreateJob(ctx, streamID, date, "recorder", false)
	if err != nil {
		return nil, err
	}
	if !existing {
		d.app.jobs.Kick()
	}
	return job, nil
}
