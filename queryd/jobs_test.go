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

func TestDedupeOverlappingMatchesKeepsBest(t *testing.T) {
	matches := []Match{
		{FileID: 6, QueryPath: "6.mp3", ResourceID: "res-1", MatchCount: 111, ReferenceStartMS: 31912, ReferenceStopMS: 185528},
		{FileID: 6, QueryPath: "6.mp3", ResourceID: "res-1", MatchCount: 23, ReferenceStartMS: 130080, ReferenceStopMS: 181016},
		{FileID: 6, QueryPath: "6.mp3", ResourceID: "res-1", MatchCount: 140, ReferenceStartMS: 34200, ReferenceStopMS: 176160},
		{FileID: 6, QueryPath: "6.mp3", ResourceID: "res-1", MatchCount: 57, ReferenceStartMS: 130247, ReferenceStopMS: 185528},
		{FileID: 6, QueryPath: "6.mp3", ResourceID: "res-1", MatchCount: 58, ReferenceStartMS: 52472, ReferenceStopMS: 107768},
	}
	out := dedupeOverlappingMatches(matches)
	if len(out) != 1 {
		t.Fatalf("matches = %d, want 1", len(out))
	}
	if out[0].MatchCount != 140 || out[0].ReferenceStartMS != 34200 {
		t.Fatalf("unexpected match: %+v", out[0])
	}
}

func TestDedupeOverlappingMatchesKeepsSeparateAirings(t *testing.T) {
	matches := []Match{
		{FileID: 6, QueryPath: "6.mp3", ResourceID: "res-1", MatchCount: 100, ReferenceStartMS: 10000, ReferenceStopMS: 40000},
		{FileID: 6, QueryPath: "6.mp3", ResourceID: "res-1", MatchCount: 90, ReferenceStartMS: 60000, ReferenceStopMS: 90000},
	}
	out := dedupeOverlappingMatches(matches)
	if len(out) != 2 {
		t.Fatalf("matches = %d, want 2", len(out))
	}
}

func TestDedupeOverlappingMatchesKeepsResourcesAndFilesSeparate(t *testing.T) {
	matches := []Match{
		{FileID: 6, QueryPath: "6.mp3", ResourceID: "res-1", MatchCount: 100, ReferenceStartMS: 10000, ReferenceStopMS: 40000},
		{FileID: 6, QueryPath: "6.mp3", ResourceID: "res-2", MatchCount: 90, ReferenceStartMS: 10000, ReferenceStopMS: 40000},
		{FileID: 7, QueryPath: "7.mp3", ResourceID: "res-1", MatchCount: 80, ReferenceStartMS: 10000, ReferenceStopMS: 40000},
	}
	out := dedupeOverlappingMatches(matches)
	if len(out) != 3 {
		t.Fatalf("matches = %d, want 3", len(out))
	}
}

func TestDedupeOverlappingMatchesAllowsTouchingIntervals(t *testing.T) {
	matches := []Match{
		{FileID: 6, QueryPath: "6.mp3", ResourceID: "res-1", MatchCount: 100, ReferenceStartMS: 0, ReferenceStopMS: 10000},
		{FileID: 6, QueryPath: "6.mp3", ResourceID: "res-1", MatchCount: 90, ReferenceStartMS: 10000, ReferenceStopMS: 20000},
	}
	out := dedupeOverlappingMatches(matches)
	if len(out) != 2 {
		t.Fatalf("matches = %d, want 2", len(out))
	}
}
