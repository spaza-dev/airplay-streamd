package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type flexInt int

func (f *flexInt) UnmarshalJSON(data []byte) error {
	value := strings.TrimSpace(string(data))
	if value == "" || value == "null" {
		*f = 0
		return nil
	}
	switch value {
	case "true":
		*f = 1
		return nil
	case "false":
		*f = 0
		return nil
	}
	value = strings.Trim(value, `"`)
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("invalid integer %q", value)
	}
	*f = flexInt(n)
	return nil
}

type RadioStation struct {
	ChangeUUID                string  `json:"changeuuid"`
	StationUUID               string  `json:"stationuuid"`
	Name                      string  `json:"name"`
	URL                       string  `json:"url"`
	URLResolved               string  `json:"url_resolved"`
	Homepage                  string  `json:"homepage"`
	Favicon                   string  `json:"favicon"`
	Tags                      string  `json:"tags"`
	Country                   string  `json:"country"`
	CountryCode               string  `json:"countrycode"`
	State                     string  `json:"state"`
	Language                  string  `json:"language"`
	LanguageCodes             string  `json:"languagecodes"`
	Votes                     int     `json:"votes"`
	LastChangeTime            string  `json:"lastchangetime"`
	LastChangeTimeISO8601     string  `json:"lastchangetime_iso8601"`
	Codec                     string  `json:"codec"`
	Bitrate                   int     `json:"bitrate"`
	HLS                       flexInt `json:"hls"`
	LastCheckOK               flexInt `json:"lastcheckok"`
	LastCheckTime             string  `json:"lastchecktime"`
	LastCheckTimeISO8601      string  `json:"lastchecktime_iso8601"`
	LastCheckOKTime           string  `json:"lastcheckoktime"`
	LastCheckOKTimeISO8601    string  `json:"lastcheckoktime_iso8601"`
	LastLocalCheckTime        string  `json:"lastlocalchecktime"`
	LastLocalCheckTimeISO8601 string  `json:"lastlocalchecktime_iso8601"`
	ClickTimestamp            string  `json:"clicktimestamp"`
	ClickTimestampISO8601     string  `json:"clicktimestamp_iso8601"`
	ClickCount                int     `json:"clickcount"`
	ClickTrend                int     `json:"clicktrend"`
	SSLError                  flexInt `json:"ssl_error"`
	GeoLat                    float64 `json:"geo_lat"`
	GeoLong                   float64 `json:"geo_long"`
	GeoDistance               float64 `json:"geo_distance"`
	HasExtendedInfo           flexInt `json:"has_extended_info"`
	ISO31662                  string  `json:"iso_3166_2"`
	ServerUUID                string  `json:"serveruuid"`
}

func (s RadioStation) IsHLS() bool     { return s.HLS != 0 }
func (s RadioStation) IsHealthy() bool { return s.LastCheckOK != 0 }
func (s RadioStation) resolvedURL() string {
	if strings.TrimSpace(s.URLResolved) != "" {
		return strings.TrimSpace(s.URLResolved)
	}
	return strings.TrimSpace(s.URL)
}

type RadioQuery struct {
	Name        string
	Country     string
	CountryCode string
	State       string
	Language    string
	Tag         string
	Codec       string
	Limit       int
	Offset      int
	HideBroken  bool
	Order       string
	Reverse     bool
}

type RadioBrowserClient struct {
	baseURL      string
	http         *http.Client
	maxResults   int
	cacheSeconds int

	mu    sync.Mutex
	cache map[string]radioCacheEntry
	now   func() time.Time
}

type radioCacheEntry struct {
	at       time.Time
	stations []RadioStation
}

func NewRadioBrowserClient(cfg DiscoveryConfig) *RadioBrowserClient {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.RadioBrowserURL), "/")
	if baseURL == "" {
		baseURL = "https://all.api.radio-browser.info"
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	maxResults := cfg.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}
	return &RadioBrowserClient{
		baseURL:      baseURL,
		http:         &http.Client{Timeout: timeout},
		maxResults:   maxResults,
		cacheSeconds: cfg.CacheSeconds,
		cache:        map[string]radioCacheEntry{},
		now:          time.Now,
	}
}

func (c *RadioBrowserClient) Search(ctx context.Context, q RadioQuery) ([]RadioStation, error) {
	if q.Limit <= 0 {
		q.Limit = 20
	}
	if q.Limit > c.maxResults {
		q.Limit = c.maxResults
	}
	if q.Limit < 1 {
		q.Limit = 1
	}
	if q.Order == "" {
		q.Order = "clickcount"
	}
	values := url.Values{}
	set := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			values.Set(key, strings.TrimSpace(value))
		}
	}
	set("name", q.Name)
	set("country", q.Country)
	set("countrycode", q.CountryCode)
	set("state", q.State)
	set("language", q.Language)
	set("tag", q.Tag)
	set("codec", q.Codec)
	values.Set("limit", strconv.Itoa(q.Limit))
	if q.Offset > 0 {
		values.Set("offset", strconv.Itoa(q.Offset))
	}
	values.Set("hidebroken", strconv.FormatBool(q.HideBroken))
	values.Set("order", q.Order)
	values.Set("reverse", strconv.FormatBool(q.Reverse))

	cacheKey := values.Encode()
	if stations, ok := c.cached(cacheKey); ok {
		return stations, nil
	}

	var stations []RadioStation
	if err := c.getJSON(ctx, "/json/stations/search?"+cacheKey, &stations); err != nil {
		return nil, err
	}
	stations = filterStations(stations, c.maxResults)
	c.storeCache(cacheKey, stations)
	return stations, nil
}

func (c *RadioBrowserClient) ByUUID(ctx context.Context, stationUUID string) (RadioStation, error) {
	if _, err := uuid.Parse(stationUUID); err != nil {
		return RadioStation{}, fmt.Errorf("invalid station uuid %q", stationUUID)
	}
	var stations []RadioStation
	path := "/json/stations/byuuid/" + url.PathEscape(stationUUID)
	if err := c.getJSON(ctx, path, &stations); err != nil {
		return RadioStation{}, err
	}
	if len(stations) == 0 {
		return RadioStation{}, ErrStreamNotFound
	}
	return stations[0], nil
}

func (c *RadioBrowserClient) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "streamd/"+version+" (radio-browser discovery)")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("radio browser request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("radio browser request failed: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("read radio browser response: %w", err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode radio browser response: %w", err)
	}
	return nil
}

func filterStations(in []RadioStation, limit int) []RadioStation {
	out := make([]RadioStation, 0, len(in))
	for _, station := range in {
		if _, err := uuid.Parse(station.StationUUID); err != nil {
			continue
		}
		if strings.TrimSpace(station.Name) == "" || station.resolvedURL() == "" {
			continue
		}
		out = append(out, station)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (c *RadioBrowserClient) cached(key string) ([]RadioStation, bool) {
	if c.cacheSeconds <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[key]
	if !ok || c.now().Sub(entry.at) > time.Duration(c.cacheSeconds)*time.Second {
		return nil, false
	}
	return append([]RadioStation(nil), entry.stations...), true
}

func (c *RadioBrowserClient) storeCache(key string, stations []RadioStation) {
	if c.cacheSeconds <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[key] = radioCacheEntry{at: c.now(), stations: append([]RadioStation(nil), stations...)}
}

type DiscoveryStation struct {
	RadioStation
	AlreadyAdded     bool   `json:"already_added"`
	ExistingStreamID string `json:"existing_stream_id,omitempty"`
}
