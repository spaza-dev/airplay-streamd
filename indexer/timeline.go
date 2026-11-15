package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const timelineVersion = 1

type Timeline struct {
	Version            int             `json:"version"`
	StreamID           string          `json:"stream_id"`
	Date               string          `json:"date"`
	Timezone           string          `json:"timezone"`
	TimelineOrigin     time.Time       `json:"timeline_origin"`
	TimelineDurationMS int64           `json:"timeline_duration_ms"`
	Recordings         []Recording     `json:"recordings"`
	Gaps               []Gap           `json:"gaps"`
	Overlaps           []Overlap       `json:"overlaps"`
	Summary            TimelineSummary `json:"summary"`
	Olaf               OlafInfo        `json:"olaf"`
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

type Gap struct {
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	TimelineStartMS int64     `json:"timeline_start_ms"`
	TimelineEndMS   int64     `json:"timeline_end_ms"`
	DurationMS      int64     `json:"duration_ms"`
	Reason          string    `json:"reason"`
}

type Overlap struct {
	FirstObjectKey  string    `json:"first_object_key"`
	SecondObjectKey string    `json:"second_object_key"`
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	TimelineStartMS int64     `json:"timeline_start_ms"`
	TimelineEndMS   int64     `json:"timeline_end_ms"`
	DurationMS      int64     `json:"duration_ms"`
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

type OlafInfo struct {
	Version               string `json:"version,omitempty"`
	IdentifierMode        string `json:"identifier_mode"`
	ReferenceOffsetUnit   string `json:"reference_offset_unit"`
	ReferenceOffsetOrigin string `json:"reference_offset_origin"`
}

type RecordingInput struct {
	ObjectKey  string
	Start      time.Time
	Size       int64
	LocalPath  string
	DurationMS int64
	ProbeError string
}

var fileNamePattern = regexp.MustCompile(`^(\d{6})\.([A-Za-z0-9]+)$`)

func ParseRecordingKey(objectKey, streamID, date string, loc *time.Location) (time.Time, error) {
	prefix := streamID + "/recordings/" + strings.ReplaceAll(date, "-", "/") + "/"
	if !strings.HasPrefix(objectKey, prefix) {
		return time.Time{}, fmt.Errorf("object %q is outside %s", objectKey, prefix)
	}
	name := strings.TrimPrefix(objectKey, prefix)
	match := fileNamePattern.FindStringSubmatch(name)
	if match == nil {
		return time.Time{}, fmt.Errorf("invalid recording filename %q", name)
	}
	base, err := time.ParseInLocation("20060102150405", strings.ReplaceAll(date, "-", "")+match[1], loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse recording timestamp %q: %w", name, err)
	}
	if base.Format("2006-01-02") != date {
		return time.Time{}, fmt.Errorf("recording %q does not belong to date %s in timezone %s", name, date, loc.String())
	}
	return base.UTC(), nil
}

func localDayBounds(date string, loc *time.Location) (time.Time, time.Time, error) {
	parsed, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	y, m, d := parsed.Date()
	origin := time.Date(y, m, d, 0, 0, 0, 0, loc)
	end := time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	return origin.UTC(), end.UTC(), nil
}

func RecordingResourceID(streamID string, start time.Time, loc *time.Location, objectKey string) string {
	local := start.In(loc)
	sum := sha256.Sum256([]byte(objectKey))
	return fmt.Sprintf("%s-%s-%s-%s", streamID, local.Format("20060102"), local.Format("150405"), hex.EncodeToString(sum[:])[:8])
}

func BuildTimeline(streamID, date string, loc *time.Location, inputs []RecordingInput, olafVersion string) (*Timeline, error) {
	origin, dayEnd, err := localDayBounds(date, loc)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(inputs, func(i, j int) bool {
		if inputs[i].Start.Equal(inputs[j].Start) {
			return inputs[i].ObjectKey < inputs[j].ObjectKey
		}
		return inputs[i].Start.Before(inputs[j].Start)
	})

	timeline := &Timeline{
		Version:        timelineVersion,
		StreamID:       streamID,
		Date:           date,
		Timezone:       loc.String(),
		TimelineOrigin: origin,
		Olaf: OlafInfo{
			Version:               olafVersion,
			IdentifierMode:        "resource_string",
			ReferenceOffsetUnit:   "seconds",
			ReferenceOffsetOrigin: "resource_start",
		},
		Recordings: []Recording{},
		Gaps:       []Gap{},
		Overlaps:   []Overlap{},
	}

	endBound := dayEnd
	for _, input := range inputs {
		duration := input.DurationMS
		status := "pending"
		if input.ProbeError != "" {
			status = "failed"
			duration = 0
		}
		entry := Recording{
			ResourceID:      RecordingResourceID(streamID, input.Start, loc, input.ObjectKey),
			ObjectKey:       input.ObjectKey,
			StartTime:       input.Start.UTC(),
			DurationMS:      duration,
			TimelineStartMS: input.Start.UTC().Sub(origin).Milliseconds(),
			TimelineEndMS:   input.Start.UTC().Sub(origin).Milliseconds() + duration,
			Status:          status,
			Error:           input.ProbeError,
			SizeBytes:       input.Size,
		}
		entry.OlafReferenceID = entry.ResourceID
		timeline.Recordings = append(timeline.Recordings, entry)
		if duration > 0 && entry.StartTime.Add(time.Duration(duration)*time.Millisecond).After(endBound) {
			endBound = entry.StartTime.Add(time.Duration(duration) * time.Millisecond)
		}
	}
	if endBound.Before(dayEnd) {
		endBound = dayEnd
	}
	timeline.TimelineDurationMS = endBound.Sub(origin).Milliseconds()

	timeline.Gaps = buildGaps(timeline.Recordings, origin, endBound)
	timeline.Overlaps = buildOverlaps(timeline.Recordings, origin)
	timeline.Summary = summarizeTimeline(timeline)
	if err := ValidateTimeline(timeline); err != nil {
		return timeline, err
	}
	return timeline, nil
}

func buildGaps(recordings []Recording, origin, end time.Time) []Gap {
	intervals := make([][2]time.Time, 0, len(recordings))
	for _, recording := range recordings {
		if recording.DurationMS <= 0 {
			continue
		}
		start := recording.StartTime
		stop := start.Add(time.Duration(recording.DurationMS) * time.Millisecond)
		intervals = append(intervals, [2]time.Time{start, stop})
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i][0].Before(intervals[j][0]) })
	merged := make([][2]time.Time, 0, len(intervals))
	for _, interval := range intervals {
		if len(merged) == 0 || interval[0].After(merged[len(merged)-1][1]) {
			merged = append(merged, interval)
			continue
		}
		if interval[1].After(merged[len(merged)-1][1]) {
			merged[len(merged)-1][1] = interval[1]
		}
	}
	var gaps []Gap
	cursor := origin
	for _, interval := range merged {
		if interval[0].After(cursor) {
			gaps = appendGap(gaps, cursor, interval[0], origin, "missing_recording")
		}
		if interval[1].After(cursor) {
			cursor = interval[1]
		}
	}
	if cursor.Before(end) {
		gaps = appendGap(gaps, cursor, end, origin, "missing_recording")
	}
	if gaps == nil {
		gaps = []Gap{}
	}
	return gaps
}

func appendGap(gaps []Gap, start, end, origin time.Time, reason string) []Gap {
	if !end.After(start) {
		return gaps
	}
	return append(gaps, Gap{
		StartTime:       start.UTC(),
		EndTime:         end.UTC(),
		TimelineStartMS: start.Sub(origin).Milliseconds(),
		TimelineEndMS:   end.Sub(origin).Milliseconds(),
		DurationMS:      end.Sub(start).Milliseconds(),
		Reason:          reason,
	})
}

func buildOverlaps(recordings []Recording, origin time.Time) []Overlap {
	type interval struct {
		key   string
		start time.Time
		end   time.Time
	}
	var intervals []interval
	for _, recording := range recordings {
		if recording.DurationMS <= 0 {
			continue
		}
		intervals = append(intervals, interval{
			key:   recording.ObjectKey,
			start: recording.StartTime,
			end:   recording.StartTime.Add(time.Duration(recording.DurationMS) * time.Millisecond),
		})
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start.Before(intervals[j].start) })
	overlaps := []Overlap{}
	maxIdx := 0
	for i := 1; i < len(intervals); i++ {
		if intervals[i].start.Before(intervals[maxIdx].end) {
			start := intervals[i].start
			end := intervals[maxIdx].end
			if intervals[i].end.Before(end) {
				end = intervals[i].end
			}
			if end.After(start) {
				overlaps = append(overlaps, Overlap{
					FirstObjectKey:  intervals[maxIdx].key,
					SecondObjectKey: intervals[i].key,
					StartTime:       start.UTC(),
					EndTime:         end.UTC(),
					TimelineStartMS: start.Sub(origin).Milliseconds(),
					TimelineEndMS:   end.Sub(origin).Milliseconds(),
					DurationMS:      end.Sub(start).Milliseconds(),
				})
			}
			if intervals[i].end.After(intervals[maxIdx].end) {
				maxIdx = i
			}
		} else {
			maxIdx = i
		}
	}
	return overlaps
}

func summarizeTimeline(timeline *Timeline) TimelineSummary {
	summary := TimelineSummary{RecordingsDiscovered: len(timeline.Recordings)}
	for _, recording := range timeline.Recordings {
		if recording.DurationMS > 0 {
			summary.TotalRecordedDuration += recording.DurationMS
		}
		switch recording.Status {
		case "indexed":
			summary.RecordingsIndexed++
		case "failed":
			summary.RecordingsFailed++
		}
	}
	for _, gap := range timeline.Gaps {
		summary.TotalGapDuration += gap.DurationMS
	}
	summary.GapCount = len(timeline.Gaps)
	summary.OverlapCount = len(timeline.Overlaps)
	return summary
}

func ValidateTimeline(timeline *Timeline) error {
	if timeline.Version <= 0 {
		return fmt.Errorf("timeline version must be positive")
	}
	if timeline.StreamID == "" || timeline.Date == "" || timeline.Timezone == "" {
		return fmt.Errorf("timeline identity fields are incomplete")
	}
	if timeline.TimelineDurationMS <= 0 {
		return fmt.Errorf("timeline duration must be positive")
	}
	seen := map[string]bool{}
	for i, recording := range timeline.Recordings {
		if recording.ResourceID == "" || recording.OlafReferenceID == "" {
			return fmt.Errorf("recording %d has no resource mapping", i)
		}
		if seen[recording.ResourceID] {
			return fmt.Errorf("duplicate resource id %q", recording.ResourceID)
		}
		seen[recording.ResourceID] = true
		if recording.StartTime.IsZero() {
			return fmt.Errorf("recording %s has no start time", recording.ResourceID)
		}
		if recording.DurationMS <= 0 && recording.Status != "failed" {
			return fmt.Errorf("recording %s has non-positive duration", recording.ResourceID)
		}
		expectedStart := recording.StartTime.Sub(timeline.TimelineOrigin).Milliseconds()
		if recording.TimelineStartMS != expectedStart {
			return fmt.Errorf("recording %s timeline start %d != expected %d", recording.ResourceID, recording.TimelineStartMS, expectedStart)
		}
		if recording.TimelineEndMS != recording.TimelineStartMS+recording.DurationMS {
			return fmt.Errorf("recording %s timeline end is inconsistent", recording.ResourceID)
		}
		if recording.TimelineStartMS < 0 {
			return fmt.Errorf("recording %s starts before timeline origin", recording.ResourceID)
		}
	}
	lastGapEnd := int64(-1)
	for _, gap := range timeline.Gaps {
		if gap.DurationMS <= 0 || gap.TimelineEndMS <= gap.TimelineStartMS {
			return fmt.Errorf("invalid gap at %d", gap.TimelineStartMS)
		}
		if gap.TimelineStartMS < lastGapEnd {
			return fmt.Errorf("gaps are not ordered")
		}
		lastGapEnd = gap.TimelineEndMS
		if gap.TimelineEndMS > timeline.TimelineDurationMS {
			return fmt.Errorf("gap extends beyond timeline")
		}
	}
	for _, overlap := range timeline.Overlaps {
		if overlap.DurationMS <= 0 {
			return fmt.Errorf("invalid overlap at %d", overlap.TimelineStartMS)
		}
	}
	summary := summarizeTimeline(timeline)
	if summary.RecordingsDiscovered != timeline.Summary.RecordingsDiscovered ||
		summary.TotalRecordedDuration != timeline.Summary.TotalRecordedDuration ||
		summary.TotalGapDuration != timeline.Summary.TotalGapDuration {
		return fmt.Errorf("timeline summary does not match inventory")
	}
	return nil
}

func TimelineChecksum(timeline *Timeline) (string, error) {
	data, err := json.Marshal(timeline)
	if err != nil {
		return "", err
	}
	return ChecksumBytes(data), nil
}

func ChecksumBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func WriteTimeline(path string, timeline *Timeline) error {
	data, err := json.MarshalIndent(timeline, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ReadTimeline(path string) (*Timeline, error) {
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
