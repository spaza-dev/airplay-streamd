package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Timeline struct {
	Version            int             `json:"version"`
	StreamID           string          `json:"stream_id"`
	Date               string          `json:"date"`
	Timezone           string          `json:"timezone"`
	TimelineOrigin     time.Time       `json:"timeline_origin"`
	TimelineDurationMS int64           `json:"timeline_duration_ms"`
	Recordings         []Recording     `json:"recordings"`
	Summary            TimelineSummary `json:"summary"`
}

type Recording struct {
	ResourceID      string    `json:"resource_id"`
	ObjectKey       string    `json:"object_key"`
	StartTime       time.Time `json:"start_time"`
	DurationMS      int64     `json:"duration_ms"`
	TimelineStartMS int64     `json:"timeline_start_ms"`
	TimelineEndMS   int64     `json:"timeline_end_ms"`
	OlafReferenceID string    `json:"olaf_reference_id"`
	Status          string    `json:"status"`
	Error           string    `json:"error,omitempty"`
	SizeBytes       int64     `json:"size_bytes,omitempty"`
}

type TimelineSummary struct {
	RecordingsDiscovered  int   `json:"recordings_discovered"`
	RecordingsIndexed     int   `json:"recordings_indexed"`
	RecordingsFailed      int   `json:"recordings_failed"`
	TotalRecordedDuration int64 `json:"total_recorded_duration_ms"`
	TotalGapDuration      int64 `json:"total_gap_duration_ms"`
	GapCount              int   `json:"gap_count"`
	OverlapCount          int   `json:"overlap_count"`
}

type IndexMeta struct {
	MetaVersion int               `json:"meta_version"`
	Status      string            `json:"status"`
	StreamID    string            `json:"stream_id"`
	Date        string            `json:"date"`
	Timezone    string            `json:"timezone"`
	BuildID     string            `json:"build_id"`
	BuildPrefix string            `json:"build_prefix"`
	Paths       map[string]string `json:"paths"`
	Index       struct {
		Files      []string         `json:"files"`
		SizesBytes map[string]int64 `json:"sizes_bytes"`
	} `json:"index"`
	Timeline struct {
		File      string `json:"file"`
		SizeBytes int64  `json:"size_bytes"`
	} `json:"timeline"`
}

type StreamMeta struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Timezone string         `json:"timezone"`
	Metadata map[string]any `json:"metadata"`
}

func (m StreamMeta) Country() string {
	if m.Metadata == nil {
		return ""
	}
	value, _ := m.Metadata["country"].(string)
	return strings.TrimSpace(value)
}

func (m StreamMeta) TimezoneOrDefault(fallback string) string {
	if strings.TrimSpace(m.Timezone) != "" {
		return strings.TrimSpace(m.Timezone)
	}
	if m.Metadata != nil {
		if value, ok := m.Metadata["timezone"].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return fallback
}

func (a *App) loadStreamMeta(ctx context.Context, streamID string) (StreamMeta, error) {
	data, err := a.storage.GetBytes(ctx, streamID+"/meta.json", 1<<20)
	if err != nil {
		return StreamMeta{}, err
	}
	var meta StreamMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return StreamMeta{}, err
	}
	return meta, nil
}

func (a *App) expandStreams(ctx context.Context, scope *Scope) ([]string, map[string]StreamMeta, error) {
	streams := make([]string, 0)
	meta := map[string]StreamMeta{}
	var candidates []string
	switch scope.Streams.Mode {
	case "ids":
		candidates = append(candidates, scope.Streams.IDs...)
	case "all", "country":
		all, err := a.storage.ListCommonPrefixes(ctx, "")
		if err != nil {
			return nil, nil, fmt.Errorf("list streams: %w", err)
		}
		for _, id := range all {
			if streamIDPattern.MatchString(id) {
				candidates = append(candidates, id)
			}
		}
	default:
		return nil, nil, fmt.Errorf("unsupported stream mode %q", scope.Streams.Mode)
	}
	for _, id := range candidates {
		if scope.Streams.Mode == "ids" {
			streams = append(streams, id)
			continue
		}
		streamMeta, err := a.loadStreamMeta(ctx, id)
		if err != nil {
			if scope.Streams.Mode == "country" {
				continue
			}
			meta[id] = StreamMeta{}
			streams = append(streams, id)
			continue
		}
		if scope.Streams.Mode == "country" && !strings.EqualFold(streamMeta.Country(), scope.Streams.Country) {
			continue
		}
		meta[id] = streamMeta
		streams = append(streams, id)
	}
	if len(streams) == 0 {
		return nil, meta, fmt.Errorf("scope resolved to no streams")
	}
	return streams, meta, nil
}

func (a *App) planIndexes(ctx context.Context, job *Job) (int, error) {
	scope, err := ParseScope(job.ScopeJSON)
	if err != nil {
		return 0, err
	}
	streams, streamMeta, err := a.expandStreams(ctx, scope)
	if err != nil {
		return 0, err
	}
	dates, err := scope.IndexDates()
	if err != nil {
		return 0, err
	}
	a.logJob(job.ID, "info", "planning", "resolved %d streams and %d index dates", len(streams), len(dates))
	inserted := 0
	for _, streamID := range streams {
		if err := a.checkInterrupt(ctx, job.ID); err != nil {
			return inserted, err
		}
		for _, date := range dates {
			if inserted >= a.cfg.Limits.MaxIndexesPerJob {
				return inserted, fmt.Errorf("scope resolves to more than %d indexes", a.cfg.Limits.MaxIndexesPerJob)
			}
			key := indexMetaKey(streamID, date)
			data, err := a.storage.GetBytes(ctx, key, 8<<20)
			if err != nil {
				continue
			}
			var meta IndexMeta
			if err := json.Unmarshal(data, &meta); err != nil {
				a.logJob(job.ID, "warn", "planning", "%s: invalid meta.json: %v", key, err)
				continue
			}
			if meta.Status != "complete" {
				continue
			}
			buildPrefix := strings.Trim(strings.TrimSpace(meta.BuildPrefix), "/")
			if buildPrefix == "" && meta.BuildID != "" {
				buildPrefix = "builds/" + meta.BuildID
			}
			if buildPrefix == "" {
				a.logJob(job.ID, "warn", "planning", "%s: missing build prefix", key)
				continue
			}
			base := streamID + "/indexes/" + datePath(date) + "/"
			fullBuild := base + buildPrefix + "/"
			size := int64(0)
			for _, bytes := range meta.Index.SizesBytes {
				size += bytes
			}
			if meta.Timeline.SizeBytes > 0 {
				size += meta.Timeline.SizeBytes
			}
			if size == 0 {
				if head, err := a.storage.Head(ctx, base+buildPrefix+"/data.mdb"); err == nil {
					size = head.Size
				}
			}
			tz := meta.Timezone
			if tz == "" {
				tz = streamMeta[streamID].TimezoneOrDefault(a.cfg.DefaultTimezone)
			}
			idx := &JobIndex{
				JobID:       job.ID,
				StreamID:    streamID,
				Date:        date,
				BuildID:     meta.BuildID,
				BuildPrefix: fullBuild,
				TimelineKey: fullBuild + "timeline.json",
				DataKey:     fullBuild + "data.mdb",
				LockKey:     fullBuild + "lock.mdb",
				Timezone:    tz,
				SizeBytes:   size,
			}
			if _, err := a.db.InsertIndex(ctx, idx); err != nil {
				return inserted, err
			}
			inserted++
		}
	}
	total, err := a.db.IndexCount(ctx, job.ID)
	if err != nil {
		return inserted, err
	}
	_ = a.db.SetJobTotals(ctx, job.ID, total, 0, 0, 0.08)
	a.logJob(job.ID, "info", "planning", "planned %d indexes", total)
	return total, nil
}

func indexMetaKey(streamID, date string) string {
	return streamID + "/indexes/" + datePath(date) + "/meta.json"
}

func datePath(date string) string {
	return strings.ReplaceAll(date, "-", "/")
}
