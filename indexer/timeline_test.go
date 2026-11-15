package main

import (
	"strings"
	"testing"
	"time"
)

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return loc
}

func TestParseRecordingKey(t *testing.T) {
	loc := mustLocation(t, "Africa/Nairobi")
	got, err := ParseRecordingKey("radio-one/recordings/2026/11/14/153000.mp3", "radio-one", "2026-11-14", loc)
	if err != nil {
		t.Fatalf("ParseRecordingKey: %v", err)
	}
	want := time.Date(2026, 11, 14, 12, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %s, want %s", got.UTC(), want)
	}
}

func TestParseRecordingKeyRejectsWrongPrefixAndName(t *testing.T) {
	loc := mustLocation(t, "Africa/Nairobi")
	cases := []string{
		"radio-two/recordings/2026/11/14/153000.mp3",
		"radio-one/recordings/2026/11/15/153000.mp3",
		"radio-one/recordings/2026/11/14/segment.mp3",
		"radio-one/recordings/2026/11/14/15300.mp3",
	}
	for _, key := range cases {
		if _, err := ParseRecordingKey(key, "radio-one", "2026-11-14", loc); err == nil {
			t.Errorf("expected error for %q", key)
		}
	}
}

func TestBuildTimelineGapsAndOverlaps(t *testing.T) {
	loc := mustLocation(t, "Africa/Nairobi")
	start := time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)
	inputs := []RecordingInput{
		{ObjectKey: "radio-one/recordings/2026/11/14/000000.mp3", Start: start, Size: 100, DurationMS: int64(time.Hour / time.Millisecond)},
		{ObjectKey: "radio-one/recordings/2026/11/14/003000.mp3", Start: start.Add(30 * time.Minute), Size: 100, DurationMS: int64(30 * time.Minute / time.Millisecond)},
		{ObjectKey: "radio-one/recordings/2026/11/14/020000.mp3", Start: start.Add(2 * time.Hour), Size: 100, DurationMS: int64(30 * time.Minute / time.Millisecond)},
	}
	timeline, err := BuildTimeline("radio-one", "2026-11-14", loc, inputs, "2026.10.19")
	if err != nil {
		t.Fatalf("BuildTimeline: %v", err)
	}
	if timeline.Summary.RecordingsDiscovered != 3 {
		t.Fatalf("discovered = %d", timeline.Summary.RecordingsDiscovered)
	}
	if timeline.Summary.OverlapCount != 1 {
		t.Fatalf("overlap count = %d, want 1", timeline.Summary.OverlapCount)
	}
	if timeline.Summary.GapCount != 2 {
		t.Fatalf("gap count = %d, want 2", timeline.Summary.GapCount)
	}
	if timeline.TimelineOrigin.Format(time.RFC3339) != "2026-11-13T21:00:00Z" {
		t.Fatalf("origin = %s", timeline.TimelineOrigin.Format(time.RFC3339))
	}
	if timeline.TimelineDurationMS != int64(24*time.Hour/time.Millisecond) {
		t.Fatalf("duration = %d", timeline.TimelineDurationMS)
	}
	if err := ValidateTimeline(timeline); err != nil {
		t.Fatalf("ValidateTimeline: %v", err)
	}
}

func TestBuildTimelineFailedRecordingDoesNotBreakValidation(t *testing.T) {
	loc := mustLocation(t, "Africa/Nairobi")
	start := time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)
	inputs := []RecordingInput{
		{ObjectKey: "radio-one/recordings/2026/11/14/000000.mp3", Start: start, ProbeError: "ffprobe failed"},
	}
	timeline, err := BuildTimeline("radio-one", "2026-11-14", loc, inputs, "2026.10.19")
	if err != nil {
		t.Fatalf("BuildTimeline: %v", err)
	}
	if timeline.Summary.RecordingsFailed != 1 {
		t.Fatalf("failed = %d", timeline.Summary.RecordingsFailed)
	}
	if timeline.Summary.GapCount != 1 {
		t.Fatalf("gap count = %d, want 1", timeline.Summary.GapCount)
	}
}

func TestRecordingResourceIDIsStableAndLocal(t *testing.T) {
	loc := mustLocation(t, "Africa/Nairobi")
	start := time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)
	first := RecordingResourceID("radio-one", start, loc, "radio-one/recordings/2026/11/14/000000.mp3")
	second := RecordingResourceID("radio-one", start, loc, "radio-one/recordings/2026/11/14/000000.mp3")
	other := RecordingResourceID("radio-one", start, loc, "radio-one/recordings/2026/11/14/000000.mp3.copy")
	if first != second {
		t.Fatalf("resource id is not stable: %s vs %s", first, second)
	}
	if first == other {
		t.Fatalf("resource id should differ for a different object key")
	}
	if !strings.HasPrefix(first, "radio-one-20261114-000000-") {
		t.Fatalf("unexpected resource id format: %s", first)
	}
}
