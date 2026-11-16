package main

import (
	"testing"
	"time"
)

func TestMapMatches(t *testing.T) {
	app := &App{cfg: &Config{DefaultTimezone: "UTC"}}
	job := &Job{ID: 1, ScopeJSON: `{"streams":{"mode":"all"},"dates":{"mode":"date","date":"2026-01-02"}}`}
	scope, err := ParseScope(job.ScopeJSON)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 2, 18, 0, 0, 0, time.UTC)
	timeline := &Timeline{
		Timezone: "UTC",
		Recordings: []Recording{{
			ResourceID: "res-1", OlafReferenceID: "olaf-1", StartTime: start,
			DurationMS: 60000, TimelineStartMS: 1000, TimelineEndMS: 61000,
		}},
	}
	idx := &JobIndex{ID: 2, StreamID: "radio", Date: "2026-01-02", Timezone: "UTC"}
	files := []JobFile{{ID: 7, OriginalName: "clip.mp3", StoredName: "7.mp3", Path: "/tmp/7.mp3"}}
	matches := []OlafMatch{{QueryPath: "7.mp3", Path: "olaf-1", ReferenceStart: 12.5, ReferenceStop: 22.5, MatchCount: 9}}
	out := app.mapMatches(job, idx, timeline, matches, files, scope)
	if len(out) != 1 {
		t.Fatalf("matches = %d", len(out))
	}
	match := out[0]
	if match.FileID != 7 || match.ResourceID != "res-1" {
		t.Fatalf("unexpected match: %+v", match)
	}
	if match.ReferenceStartMS != 12500 || match.ReferenceStopMS != 22500 {
		t.Fatalf("unexpected offsets: %+v", match)
	}
	want := start.Add(12500 * time.Millisecond)
	if !match.Occurrence.Equal(want) {
		t.Fatalf("occurrence = %s, want %s", match.Occurrence, want)
	}
	if match.TimelineStartMS != 13500 || match.TimelineEndMS != 23500 {
		t.Fatalf("unexpected timeline: %+v", match)
	}
}

func TestMapMatchesFiltersDates(t *testing.T) {
	app := &App{cfg: &Config{DefaultTimezone: "UTC"}}
	job := &Job{ID: 1, ScopeJSON: `{"streams":{"mode":"all"},"dates":{"mode":"date","date":"2026-01-03"}}`}
	scope, _ := ParseScope(job.ScopeJSON)
	timeline := &Timeline{
		Timezone: "UTC",
		Recordings: []Recording{{
			ResourceID: "res-1", StartTime: time.Date(2026, 1, 2, 23, 0, 0, 0, time.UTC),
			DurationMS: 3600000, TimelineStartMS: 0, TimelineEndMS: 3600000,
		}},
	}
	idx := &JobIndex{ID: 2, StreamID: "radio", Date: "2026-01-02"}
	matches := []OlafMatch{{Path: "res-1", ReferenceStart: 10, ReferenceStop: 20}}
	out := app.mapMatches(job, idx, timeline, matches, nil, scope)
	if len(out) != 0 {
		t.Fatalf("expected no matches, got %d", len(out))
	}
}
