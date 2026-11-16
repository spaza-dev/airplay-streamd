package main

import (
	"testing"
	"time"
)

func TestParseScopeValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"all range", `{"streams":{"mode":"all"},"dates":{"mode":"range","from":"2026-01-01","to":"2026-01-03"}}`, true},
		{"ids", `{"streams":{"mode":"ids","ids":["radio-one"]},"dates":{"mode":"date","date":"2026-01-01"}}`, true},
		{"country", `{"streams":{"mode":"country","country":"KE"},"dates":{"mode":"date","date":"2026-01-01"}}`, true},
		{"missing ids", `{"streams":{"mode":"ids"},"dates":{"mode":"date","date":"2026-01-01"}}`, false},
		{"bad date", `{"streams":{"mode":"all"},"dates":{"mode":"date","date":"01-01-2026"}}`, false},
		{"bad range", `{"streams":{"mode":"all"},"dates":{"mode":"range","from":"2026-02-01","to":"2026-01-01"}}`, false},
		{"bad window", `{"streams":{"mode":"all"},"dates":{"mode":"date","date":"2026-01-01"},"daily_time_window":{"start":"25:00","end":"02:00"}}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseScope(test.raw)
			if test.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !test.ok && err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestIndexDatesIncludePreviousDay(t *testing.T) {
	scope := &Scope{
		Streams: StreamScope{Mode: "all"},
		Dates:   DateScope{Mode: "range", From: "2026-01-02", To: "2026-01-03"},
	}
	dates, err := scope.IndexDates()
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"2026-01-01", "2026-01-02", "2026-01-03"}
	if len(dates) != len(expected) {
		t.Fatalf("dates = %v", dates)
	}
	for i := range expected {
		if dates[i] != expected[i] {
			t.Fatalf("dates[%d] = %s, want %s", i, dates[i], expected[i])
		}
	}
}

func TestAllowsOccurrenceDateAndWindow(t *testing.T) {
	scope := &Scope{
		Streams: StreamScope{Mode: "all"},
		Dates:   DateScope{Mode: "date", Date: "2026-01-02"},
		DailyTimeWindow: &TimeWindow{
			Start: "22:00", End: "02:00", Basis: "stream_local", Match: "overlap",
		},
	}
	cases := []struct {
		when time.Time
		want bool
	}{
		{time.Date(2026, 1, 2, 21, 59, 0, 0, time.UTC), false},
		{time.Date(2026, 1, 2, 23, 30, 0, 0, time.UTC), true},
		{time.Date(2026, 1, 3, 1, 0, 0, 0, time.UTC), true},
		{time.Date(2026, 1, 3, 2, 1, 0, 0, time.UTC), false},
		{time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC), false},
	}
	for _, test := range cases {
		if got := scope.AllowsOccurrence(test.when, time.Minute, "UTC"); got != test.want {
			t.Fatalf("AllowsOccurrence(%s) = %v, want %v", test.when, got, test.want)
		}
	}
}
