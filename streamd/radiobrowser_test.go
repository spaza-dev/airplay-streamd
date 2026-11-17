package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func TestRadioBrowserSearchAndCache(t *testing.T) {
	stationID := uuid.NewString()
	var requests int32
	var gotQuery map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if r.URL.Path != "/json/stations/search" {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotQuery = map[string]string{}
		for key := range r.URL.Query() {
			gotQuery[key] = r.URL.Query().Get(key)
		}
		if !strings.Contains(r.Header.Get("User-Agent"), "streamd/") {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		writeJSON(rw, http.StatusOK, []map[string]any{
			{
				"stationuuid":       stationID,
				"name":              "Capital FM",
				"url":               "https://example.com/capital",
				"url_resolved":      "https://example.com/capital.mp3",
				"country":           "Kenya",
				"countrycode":       "KE",
				"codec":             "MP3",
				"bitrate":           128,
				"hls":               0,
				"lastcheckok":       1,
				"has_extended_info": true,
			},
			{
				"stationuuid": "not-a-uuid",
				"name":        "Invalid",
				"url":         "https://example.com/invalid",
			},
			{
				"stationuuid": uuid.NewString(),
				"name":        "Missing URL",
				"url":         "",
			},
		})
	}))
	defer srv.Close()

	client := NewRadioBrowserClient(DiscoveryConfig{
		RadioBrowserURL: srv.URL,
		TimeoutSeconds:  5,
		CacheSeconds:    60,
		MaxResults:      10,
	})
	q := RadioQuery{Name: "Capital", Limit: 5, HideBroken: true, Order: "clickcount", Reverse: true}
	stations, err := client.Search(context.Background(), q)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(stations) != 1 || stations[0].StationUUID != stationID {
		t.Fatalf("stations = %+v", stations)
	}
	if !stations[0].IsHealthy() || stations[0].IsHLS() {
		t.Fatalf("station flags = %+v", stations[0])
	}
	if gotQuery["name"] != "Capital" || gotQuery["hidebroken"] != "true" ||
		gotQuery["limit"] != "5" || gotQuery["order"] != "clickcount" ||
		gotQuery["reverse"] != "true" {
		t.Fatalf("query = %+v", gotQuery)
	}

	if _, err := client.Search(context.Background(), q); err != nil {
		t.Fatalf("cached search: %v", err)
	}
	if atomic.LoadInt32(&requests) != 1 {
		t.Fatalf("requests = %d, want 1 (cache miss)", requests)
	}
}

func TestRadioBrowserByUUID(t *testing.T) {
	stationID := uuid.NewString()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/stations/byuuid/"+stationID {
			t.Errorf("path = %q", r.URL.Path)
		}
		writeJSON(rw, http.StatusOK, []RadioStation{{
			StationUUID: stationID,
			Name:        "Kiss FM",
			URL:         "https://example.com/kiss",
			HLS:         flexInt(1),
			LastCheckOK: flexInt(0),
		}})
	}))
	defer srv.Close()

	client := NewRadioBrowserClient(DiscoveryConfig{RadioBrowserURL: srv.URL, TimeoutSeconds: 5})
	station, err := client.ByUUID(context.Background(), stationID)
	if err != nil {
		t.Fatalf("by uuid: %v", err)
	}
	if station.Name != "Kiss FM" || !station.IsHLS() || station.IsHealthy() {
		t.Fatalf("station = %+v", station)
	}
	if _, err := client.ByUUID(context.Background(), "not-a-uuid"); err == nil {
		t.Fatal("invalid uuid accepted")
	}
}

func TestRadioBrowserUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, "nope", http.StatusBadGateway)
	}))
	defer srv.Close()

	client := NewRadioBrowserClient(DiscoveryConfig{RadioBrowserURL: srv.URL, TimeoutSeconds: 5})
	if _, err := client.Search(context.Background(), RadioQuery{Name: "x"}); err == nil {
		t.Fatal("expected upstream error")
	}
}

func TestFlexIntAcceptsNumbersAndBooleans(t *testing.T) {
	var value struct {
		Number flexInt `json:"number"`
		Bool   flexInt `json:"bool"`
		String flexInt `json:"string"`
	}
	if err := json.Unmarshal([]byte(`{"number":1,"bool":true,"string":"0"}`), &value); err != nil {
		t.Fatal(err)
	}
	if value.Number != 1 || value.Bool != 1 || value.String != 0 {
		t.Fatalf("values = %+v", value)
	}
}
