package main

import "testing"

func TestParseOlafOutputObject(t *testing.T) {
	raw := []byte(`{
	  "query_path": "/tmp/1.mp3",
	  "matches": [
	    {"path":"radio/recordings/2026/01/02/180000.ogg","match_count":42,"reference_start":12.5,"reference_stop":22.5,"score":0.9}
	  ]
	}`)
	matches, err := parseOlafOutput(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d", len(matches))
	}
	match := matches[0]
	if match.QueryPath != "/tmp/1.mp3" || match.Path != "radio/recordings/2026/01/02/180000.ogg" {
		t.Fatalf("unexpected match: %+v", match)
	}
	if match.ReferenceStart != 12.5 || match.ReferenceStop != 22.5 || match.MatchCount != 42 {
		t.Fatalf("unexpected values: %+v", match)
	}
}

func TestParseOlafOutputStream(t *testing.T) {
	raw := []byte(`{"query_path":"1.mp3","matches":[{"path":"a.ogg","match_count":3,"reference_start":1}]}
{"query_path":"2.mp3","matches":[{"path":"b.ogg","match_count":4,"reference_start":2}]}`)
	matches, err := parseOlafOutput(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %d", len(matches))
	}
	if matches[1].QueryPath != "2.mp3" || matches[1].Path != "b.ogg" {
		t.Fatalf("unexpected second match: %+v", matches[1])
	}
}
