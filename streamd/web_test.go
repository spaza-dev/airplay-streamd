package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newWebTestApp(t *testing.T, streams []StreamConfig, token string) (*App, *WebServer) {
	t.Helper()
	dir := t.TempDir()
	cfg := validTestConfig(t, dir)
	cfg.Server.AdminToken = token
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}

	logger, err := NewLogger("error", "")
	if err != nil {
		t.Fatal(err)
	}
	state := &RuntimeState{Stopped: map[string]bool{}}
	store, err := OpenStreamStore(cfg.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	app := newApp(filepath.Join(dir, "config.yaml"), "", &cfg, logger, state, filepath.Join(dir, "runtime.json"))
	app.streams = store
	for i := range streams {
		if _, err := uuid.Parse(streams[i].ID); err != nil {
			streams[i].ID = testID(streams[i].ID)
		}
		if streams[i].Source == "" {
			streams[i].Source = "custom"
		}
		if err := store.Create(t.Context(), streams[i]); err != nil {
			t.Fatalf("seed stream: %v", err)
		}
	}
	app.recorder = NewManager(app)
	t.Cleanup(func() {
		_ = store.Close()
		_ = logger.Close()
	})
	return app, NewWebServer(app)
}

func serveRequest(t *testing.T, ws *WebServer, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	handler, err := ws.handler()
	if err != nil {
		t.Fatalf("build handler: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestPublicStreamsListsOnlyEnabledWithOverview(t *testing.T) {
	streams := []StreamConfig{
		{
			ID: "enabled-a", Name: "Enabled A", Type: "radio",
			URL: "https://example.com/a", Enabled: true,
			Audio: &AudioConfig{
				Codec: "libopus", Container: "ogg", Bitrate: "64k",
				SampleRate: 48000, Channels: 1,
			},
		},
		{ID: "disabled-b", Name: "Disabled B", Type: "tv", URL: "https://example.com/b", Enabled: false},
		{ID: "error-c", Name: "Error C", Type: "hls", URL: "https://example.com/c", Enabled: true},
		{ID: "stopped-d", Name: "Stopped D", Type: "direct", URL: "https://example.com/d", Enabled: true},
		{ID: "idle-e", Name: "Idle E", Type: "web", URL: "https://example.com/e", Enabled: true},
	}
	app, ws := newWebTestApp(t, streams, "")

	activity := time.Date(2026, 11, 16, 10, 5, 0, 0, time.UTC)
	app.recorder.workers[testID("enabled-a")] = &Worker{app: app, running: true, lastActivity: activity}
	app.recorder.workers[testID("error-c")] = &Worker{app: app, running: true, errMsg: "source unavailable"}
	app.state.Stopped[testID("stopped-d")] = true

	rr := serveRequest(t, ws, "/api/streams", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type = %q", ct)
	}

	var response PublicStreamsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Streams) != 4 {
		t.Fatalf("stream count = %d: %+v", len(response.Streams), response.Streams)
	}
	ids := make([]string, 0, len(response.Streams))
	for _, stream := range response.Streams {
		ids = append(ids, stream.ID)
	}
	if got, want := strings.Join(ids, ","), testID("enabled-a")+","+testID("error-c")+","+testID("idle-e")+","+testID("stopped-d"); got != want {
		t.Fatalf("ids = %s, want %s", got, want)
	}

	byID := map[string]PublicStreamView{}
	for _, stream := range response.Streams {
		byID[stream.ID] = stream
	}
	a := byID[testID("enabled-a")]
	if a.Status != "running" || a.Type != "radio" || a.URL != "https://example.com/a" || !a.Enabled {
		t.Fatalf("unexpected enabled stream: %+v", a)
	}
	if a.LastActivity == nil || !a.LastActivity.Equal(activity) {
		t.Fatalf("last activity = %v", a.LastActivity)
	}
	if a.Audio == nil || a.Audio.Codec != "libopus" || a.Audio.Bitrate != "64k" || a.Audio.Channels != 1 {
		t.Fatalf("audio = %+v", a.Audio)
	}
	if a.Video == nil || videoEnabled(*a.Video) {
		t.Fatalf("video default not merged: %+v", a.Video)
	}
	if byID[testID("error-c")].Status != "error" || byID[testID("error-c")].Error != "source unavailable" {
		t.Fatalf("error stream = %+v", byID[testID("error-c")])
	}
	if byID[testID("stopped-d")].Status != "stopped" {
		t.Fatalf("stopped stream = %+v", byID[testID("stopped-d")])
	}
	if byID[testID("idle-e")].Status != "idle" {
		t.Fatalf("idle stream = %+v", byID[testID("idle-e")])
	}
	if _, ok := byID[testID("disabled-b")]; ok {
		t.Fatal("disabled stream was returned")
	}

	var raw struct {
		Streams []map[string]any `json:"streams"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	for _, stream := range raw.Streams {
		if _, ok := stream["chunk_seconds"]; ok {
			t.Fatalf("chunk_seconds leaked in response: %+v", stream)
		}
		if _, ok := stream["last_activity"]; !ok {
			t.Fatalf("last_activity missing from response: %+v", stream)
		}
	}
}

func TestPublicStreamsAuthentication(t *testing.T) {
	streams := []StreamConfig{
		{ID: testID("one"), Type: "radio", URL: "https://example.com/one", Enabled: true},
	}

	_, openWS := newWebTestApp(t, streams, "")
	if rr := serveRequest(t, openWS, "/api/streams", nil); rr.Code != http.StatusOK {
		t.Fatalf("open API status = %d", rr.Code)
	}

	_, ws := newWebTestApp(t, streams, "secret-token")
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "wrong bearer", headers: map[string]string{"Authorization": "Bearer wrong"}, want: http.StatusUnauthorized},
		{name: "bearer", headers: map[string]string{"Authorization": "Bearer secret-token"}, want: http.StatusOK},
		{name: "header", headers: map[string]string{"X-Admin-Token": "secret-token"}, want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := serveRequest(t, ws, "/api/streams", tc.headers)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d, body=%s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}

	if rr := serveRequest(t, ws, "/health", nil); rr.Code != http.StatusOK {
		t.Fatalf("health status = %d", rr.Code)
	}
	if rr := serveRequest(t, ws, "/control/streams", nil); rr.Code != http.StatusOK {
		t.Fatalf("control API status = %d", rr.Code)
	}
}

func TestOpenAPIDocumentAndDocs(t *testing.T) {
	_, ws := newWebTestApp(t, nil, "")

	rr := serveRequest(t, ws, "/openapi.json", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("openapi status = %d", rr.Code)
	}
	if !json.Valid(rr.Body.Bytes()) {
		t.Fatalf("openapi document is not valid JSON: %s", rr.Body.String())
	}
	var doc struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode openapi: %v", err)
	}
	if _, ok := doc.Paths["/api/streams"]; !ok {
		t.Fatalf("openapi missing /api/streams: %+v", doc.Paths)
	}

	rr = serveRequest(t, ws, "/docs", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "swagger-ui") {
		t.Fatalf("docs status = %d body=%s", rr.Code, rr.Body.String())
	}

	rr = serveRequest(t, ws, "/api/nope", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown API status = %d body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("unknown API content type = %q", ct)
	}
}

func serveRequestMethod(t *testing.T, ws *WebServer, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	handler, err := ws.handler()
	if err != nil {
		t.Fatalf("build handler: %v", err)
	}
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func seedMetrics(app *App) {
	now := time.Now()
	collector := &MetricsCollector{app: app, maxHistory: 10}
	current := &MetricPoint{
		Time:             now,
		CPUPercent:       95,
		MemoryUsedBytes:  2 * 1024 * 1024 * 1024,
		MemoryTotalBytes: 8 * 1024 * 1024 * 1024,
		DiskFreeBytes:    100 * 1024 * 1024 * 1024,
		DiskTotalBytes:   200 * 1024 * 1024 * 1024,
		NetRxBytesPerSec: 1000,
		NetTxBytesPerSec: 1000,
		ActiveStreams:    1,
		BacklogBytes:     1000,
	}
	collector.current = current
	collector.history = []MetricPoint{
		{Time: now.Add(-10 * time.Second), CPUPercent: 90, BacklogBytes: 1000},
		*current,
	}
	collector.streams = []StreamMetrics{
		{ID: testID("one"), PID: 123, CPUPercent: 20, MemoryBytes: 512 * 1024 * 1024, OutputBytesPerSec: 16000},
	}
	app.metrics = collector
}

func TestMetricsEndpointAndCapacityEnforcement(t *testing.T) {
	streams := []StreamConfig{
		{ID: testID("one"), Name: "One", Type: "radio", URL: "https://example.com/one", Enabled: true},
	}
	app, ws := newWebTestApp(t, streams, "")
	seedMetrics(app)

	rr := serveRequest(t, ws, "/control/metrics", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("metrics status = %d body=%s", rr.Code, rr.Body.String())
	}
	var snapshot MetricsSnapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if !snapshot.Enabled || snapshot.Current == nil {
		t.Fatalf("unexpected metrics snapshot: %+v", snapshot)
	}
	if snapshot.Capacity.Status != "at_capacity" {
		t.Fatalf("capacity status = %q", snapshot.Capacity.Status)
	}

	blocked := serveRequestMethod(t, ws, http.MethodPost, "/control/streams/"+testID("one")+"/start")
	if blocked.Code != http.StatusConflict {
		t.Fatalf("blocked start status = %d body=%s", blocked.Code, blocked.Body.String())
	}
	var payload struct {
		Error    string           `json:"error"`
		Capacity CapacityEstimate `json:"capacity"`
	}
	if err := json.Unmarshal(blocked.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode blocked response: %v", err)
	}
	if payload.Error == "" || payload.Capacity.Status != "at_capacity" {
		t.Fatalf("blocked payload = %+v", payload)
	}

	app.recorder.workers[testID("one")] = &Worker{app: app, running: true}
	forced := serveRequestMethod(t, ws, http.MethodPost, "/control/streams/"+testID("one")+"/start?force=true")
	if forced.Code != http.StatusOK {
		t.Fatalf("forced start status = %d body=%s", forced.Code, forced.Body.String())
	}
}

func TestDiscoveryEndpoints(t *testing.T) {
	addedID := uuid.NewString()
	var upstreamPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		writeJSON(rw, http.StatusOK, []RadioStation{{
			StationUUID: addedID,
			Name:        "Capital FM",
			URL:         "https://example.com/capital",
			Country:     "Kenya",
			Codec:       "MP3",
			Bitrate:     128,
			LastCheckOK: flexInt(1),
		}})
	}))
	defer upstream.Close()

	app, ws := newWebTestApp(t, []StreamConfig{{
		ID: addedID, Source: "radio-browser", Name: "Capital FM", Type: "radio",
		URL: "https://example.com/capital", Enabled: true,
		Radio: &RadioStation{StationUUID: addedID, Name: "Capital FM", URL: "https://example.com/capital"},
	}}, "")
	app.discovery = NewRadioBrowserClient(DiscoveryConfig{
		RadioBrowserURL: upstream.URL,
		TimeoutSeconds:  5,
		CacheSeconds:    0,
		MaxResults:      10,
	})

	rr := serveRequest(t, ws, "/control/discovery/radio?name=Capital&limit=5", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", rr.Code, rr.Body.String())
	}
	var response struct {
		Stations []DiscoveryStation `json:"stations"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Stations) != 1 || !response.Stations[0].AlreadyAdded ||
		response.Stations[0].ExistingStreamID != addedID {
		t.Fatalf("stations = %+v", response.Stations)
	}
	if upstreamPath != "/json/stations/search" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}

	rr = serveRequest(t, ws, "/control/discovery/radio/"+addedID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("by uuid status = %d body=%s", rr.Code, rr.Body.String())
	}
	var station DiscoveryStation
	if err := json.Unmarshal(rr.Body.Bytes(), &station); err != nil {
		t.Fatal(err)
	}
	if station.StationUUID != addedID || !station.AlreadyAdded {
		t.Fatalf("station = %+v", station)
	}
	if upstreamPath != "/json/stations/byuuid/"+addedID {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
}

func TestDiscoveryDisabled(t *testing.T) {
	_, ws := newWebTestApp(t, nil, "")
	rr := serveRequest(t, ws, "/control/discovery/radio?name=x", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAddStreamAssignsUUIDs(t *testing.T) {
	app, _ := newWebTestApp(t, nil, "")

	custom, _, err := app.AddStream(StreamConfig{
		ID: "client-supplied", Name: "Custom", Type: "direct",
		URL: "https://example.com/custom.ogg",
	})
	if err != nil {
		t.Fatalf("add custom: %v", err)
	}
	if custom.ID == "client-supplied" {
		t.Fatal("client-supplied id was accepted")
	}
	if _, err := uuid.Parse(custom.ID); err != nil {
		t.Fatalf("generated id = %q: %v", custom.ID, err)
	}
	if custom.Source != "custom" {
		t.Fatalf("source = %q", custom.Source)
	}

	stationUUID := uuid.NewString()
	radio, _, err := app.AddStream(StreamConfig{
		Radio: &RadioStation{
			StationUUID: stationUUID,
			Name:        "Capital FM",
			URL:         "https://example.com/capital",
			Country:     "Kenya",
			CountryCode: "KE",
			Codec:       "MP3",
			Bitrate:     128,
			LastCheckOK: flexInt(1),
		},
	})
	if err != nil {
		t.Fatalf("add radio: %v", err)
	}
	if radio.ID != stationUUID || radio.Source != "radio-browser" {
		t.Fatalf("radio stream = %+v", radio)
	}
	if radio.Metadata["source"] != "radio-browser" || radio.Metadata["country"] != "Kenya" {
		t.Fatalf("radio metadata = %+v", radio.Metadata)
	}
	if _, _, err := app.AddStream(StreamConfig{
		Radio: &RadioStation{StationUUID: stationUUID, Name: "Capital FM", URL: "https://example.com/capital"},
	}); !errors.Is(err, ErrStreamConflict) {
		t.Fatalf("duplicate station error = %v", err)
	}
}
