package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newWebTestApp(t *testing.T, streams []StreamConfig, token string) (*App, *WebServer) {
	t.Helper()
	dir := t.TempDir()
	cfg := validTestConfig(t, dir)
	cfg.Server.AdminToken = token
	cfg.Streams = streams
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}

	logger, err := NewLogger("error", "")
	if err != nil {
		t.Fatal(err)
	}
	state := &RuntimeState{Stopped: map[string]bool{}, MetaCreatedAt: map[string]string{}}
	app := newApp(filepath.Join(dir, "config.yaml"), "", &cfg, logger, state, filepath.Join(dir, "runtime.json"))
	app.recorder = NewManager(app)
	t.Cleanup(func() { _ = logger.Close() })
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
	app.recorder.workers["enabled-a"] = &Worker{app: app, running: true, lastActivity: activity}
	app.recorder.workers["error-c"] = &Worker{app: app, running: true, errMsg: "source unavailable"}
	app.state.Stopped["stopped-d"] = true

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
	if got, want := strings.Join(ids, ","), "enabled-a,error-c,idle-e,stopped-d"; got != want {
		t.Fatalf("ids = %s, want %s", got, want)
	}

	byID := map[string]PublicStreamView{}
	for _, stream := range response.Streams {
		byID[stream.ID] = stream
	}
	a := byID["enabled-a"]
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
	if byID["error-c"].Status != "error" || byID["error-c"].Error != "source unavailable" {
		t.Fatalf("error stream = %+v", byID["error-c"])
	}
	if byID["stopped-d"].Status != "stopped" {
		t.Fatalf("stopped stream = %+v", byID["stopped-d"])
	}
	if byID["idle-e"].Status != "idle" {
		t.Fatalf("idle stream = %+v", byID["idle-e"])
	}
	if _, ok := byID["disabled-b"]; ok {
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
		{ID: "one", Type: "radio", URL: "https://example.com/one", Enabled: true},
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
