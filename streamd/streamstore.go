package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrStreamNotFound = errors.New("stream not found")
	ErrStreamConflict = errors.New("stream already exists")
)

type StreamStore struct {
	db      *sql.DB
	writeMu sync.Mutex
}

func OpenStreamStore(path string) (*StreamStore, error) {
	db, err := openSQLiteDB(path)
	if err != nil {
		return nil, err
	}
	if err := migrateStreamStore(db); err != nil {
		db.Close()
		return nil, err
	}
	return &StreamStore{db: db}, nil
}

func (s *StreamStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

const streamSchema = `
CREATE TABLE IF NOT EXISTS streams (
    id            TEXT PRIMARY KEY,
    source        TEXT NOT NULL DEFAULT 'custom'
                  CHECK (source IN ('radio-browser', 'custom')),
    stream_type   TEXT NOT NULL DEFAULT 'radio',
    enabled       INTEGER NOT NULL DEFAULT 0,
    chunk_seconds INTEGER NOT NULL DEFAULT 0,
    audio_json    TEXT,
    video_json    TEXT,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,

    stationuuid                TEXT,
    changeuuid                 TEXT,
    serveruuid                 TEXT,
    name                       TEXT NOT NULL,
    url                        TEXT NOT NULL,
    url_resolved               TEXT,
    homepage                   TEXT,
    favicon                    TEXT,
    tags                       TEXT,
    country                    TEXT,
    countrycode                TEXT,
    state                      TEXT,
    language                   TEXT,
    languagecodes              TEXT,
    votes                      INTEGER,
    lastchangetime             TEXT,
    lastchangetime_iso8601     TEXT,
    codec                      TEXT,
    bitrate                    INTEGER,
    hls                        INTEGER NOT NULL DEFAULT 0,
    lastcheckok                INTEGER NOT NULL DEFAULT 0,
    lastchecktime              TEXT,
    lastchecktime_iso8601      TEXT,
    lastcheckoktime            TEXT,
    lastcheckoktime_iso8601    TEXT,
    lastlocalchecktime         TEXT,
    lastlocalchecktime_iso8601 TEXT,
    clicktimestamp             TEXT,
    clicktimestamp_iso8601     TEXT,
    clickcount                 INTEGER,
    clicktrend                 INTEGER,
    ssl_error                  INTEGER,
    geo_lat                    REAL,
    geo_long                   REAL,
    geo_distance               REAL,
    has_extended_info          INTEGER,
    iso_3166_2                 TEXT,
    raw_json                   TEXT,

    CHECK (
        source <> 'radio-browser'
        OR (stationuuid IS NOT NULL AND stationuuid = id)
    )
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_streams_stationuuid
    ON streams(stationuuid) WHERE stationuuid IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_streams_name        ON streams(name);
CREATE INDEX IF NOT EXISTS idx_streams_country     ON streams(country);
CREATE INDEX IF NOT EXISTS idx_streams_countrycode ON streams(countrycode);
CREATE INDEX IF NOT EXISTS idx_streams_codec       ON streams(codec);
CREATE INDEX IF NOT EXISTS idx_streams_enabled     ON streams(enabled);
CREATE INDEX IF NOT EXISTS idx_streams_source      ON streams(source);
`

func migrateStreamStore(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("stream database schema version %d is newer than supported", version)
	}
	if _, err := db.Exec(streamSchema); err != nil {
		return err
	}
	if version < 1 {
		if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
			return err
		}
	}
	return nil
}

const streamColumns = `id, source, stream_type, enabled, chunk_seconds,
    audio_json, video_json, metadata_json, created_at, updated_at,
    stationuuid, changeuuid, serveruuid, name, url, url_resolved, homepage, favicon,
    tags, country, countrycode, state, language, languagecodes, votes,
    lastchangetime, lastchangetime_iso8601, codec, bitrate, hls, lastcheckok,
    lastchecktime, lastchecktime_iso8601, lastcheckoktime, lastcheckoktime_iso8601,
    lastlocalchecktime, lastlocalchecktime_iso8601, clicktimestamp,
    clicktimestamp_iso8601, clickcount, clicktrend, ssl_error,
    geo_lat, geo_long, geo_distance, has_extended_info, iso_3166_2, raw_json`

const streamUpdateColumns = `source = ?, stream_type = ?, enabled = ?, chunk_seconds = ?,
    audio_json = ?, video_json = ?, metadata_json = ?, created_at = ?, updated_at = ?,
    stationuuid = ?, changeuuid = ?, serveruuid = ?, name = ?, url = ?, url_resolved = ?,
    homepage = ?, favicon = ?, tags = ?, country = ?, countrycode = ?, state = ?,
    language = ?, languagecodes = ?, votes = ?, lastchangetime = ?,
    lastchangetime_iso8601 = ?, codec = ?, bitrate = ?, hls = ?, lastcheckok = ?,
    lastchecktime = ?, lastchecktime_iso8601 = ?, lastcheckoktime = ?,
    lastcheckoktime_iso8601 = ?, lastlocalchecktime = ?,
    lastlocalchecktime_iso8601 = ?, clicktimestamp = ?, clicktimestamp_iso8601 = ?,
    clickcount = ?, clicktrend = ?, ssl_error = ?, geo_lat = ?, geo_long = ?,
    geo_distance = ?, has_extended_info = ?, iso_3166_2 = ?, raw_json = ?`

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func (s *StreamStore) Create(ctx context.Context, stream StreamConfig) error {
	if stream.Source == "" {
		stream.Source = "custom"
	}
	now := time.Now().UTC()
	if stream.CreatedAt.IsZero() {
		stream.CreatedAt = now
	}
	stream.UpdatedAt = now
	args := streamArgs(stream)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO streams (`+streamColumns+`) VALUES (`+placeholders(len(args))+`)`,
		args...,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return fmt.Errorf("%w: %v", ErrStreamConflict, err)
		}
		return err
	}
	return nil
}

func (s *StreamStore) Update(ctx context.Context, stream StreamConfig) error {
	if stream.Source == "" {
		stream.Source = "custom"
	}
	stream.UpdatedAt = time.Now().UTC()
	args := streamArgs(stream)
	id := args[0]
	args = args[1:]
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx,
		`UPDATE streams SET `+streamUpdateColumns+` WHERE id = ?`,
		append(args, id)...,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return fmt.Errorf("%w: %v", ErrStreamConflict, err)
		}
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrStreamNotFound
	}
	return nil
}

func (s *StreamStore) Delete(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM streams WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrStreamNotFound
	}
	return nil
}

func (s *StreamStore) Get(ctx context.Context, id string) (StreamConfig, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, source, stream_type, enabled, chunk_seconds,
		        audio_json, video_json, metadata_json, created_at, updated_at,
		        name, url, stationuuid, raw_json
		 FROM streams WHERE id = ?`, id)
	stream, err := scanStream(row)
	if errors.Is(err, sql.ErrNoRows) {
		return StreamConfig{}, false, nil
	}
	return stream, err == nil, err
}

func (s *StreamStore) GetByStationUUID(ctx context.Context, stationUUID string) (StreamConfig, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, source, stream_type, enabled, chunk_seconds,
		        audio_json, video_json, metadata_json, created_at, updated_at,
		        name, url, stationuuid, raw_json
		 FROM streams WHERE stationuuid = ?`, stationUUID)
	stream, err := scanStream(row)
	if errors.Is(err, sql.ErrNoRows) {
		return StreamConfig{}, false, nil
	}
	return stream, err == nil, err
}

func (s *StreamStore) List(ctx context.Context) ([]StreamConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, source, stream_type, enabled, chunk_seconds,
		        audio_json, video_json, metadata_json, created_at, updated_at,
		        name, url, stationuuid, raw_json
		 FROM streams ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]StreamConfig, 0)
	for rows.Next() {
		stream, err := scanStream(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, stream)
	}
	return out, rows.Err()
}

func (s *StreamStore) StationUUIDs(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT stationuuid, id FROM streams WHERE stationuuid IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var stationUUID, id string
		if err := rows.Scan(&stationUUID, &id); err != nil {
			return nil, err
		}
		out[stationUUID] = id
	}
	return out, rows.Err()
}

type streamScanner interface {
	Scan(dest ...any) error
}

func scanStream(scanner streamScanner) (StreamConfig, error) {
	var (
		stream     StreamConfig
		enabled    int
		audioJSON  sql.NullString
		videoJSON  sql.NullString
		metadata   sql.NullString
		createdRaw string
		updatedRaw string
		station    sql.NullString
		rawJSON    sql.NullString
	)
	err := scanner.Scan(
		&stream.ID, &stream.Source, &stream.Type, &enabled, &stream.ChunkSeconds,
		&audioJSON, &videoJSON, &metadata, &createdRaw, &updatedRaw,
		&stream.Name, &stream.URL, &station, &rawJSON,
	)
	if err != nil {
		return StreamConfig{}, err
	}
	stream.Enabled = enabled != 0
	if stream.Source == "" {
		stream.Source = "custom"
	}
	if createdRaw != "" {
		stream.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
	}
	if updatedRaw != "" {
		stream.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
	}
	if audioJSON.Valid && audioJSON.String != "" {
		var audio AudioConfig
		if err := json.Unmarshal([]byte(audioJSON.String), &audio); err == nil {
			stream.Audio = &audio
		}
	}
	if videoJSON.Valid && videoJSON.String != "" {
		var video VideoConfig
		if err := json.Unmarshal([]byte(videoJSON.String), &video); err == nil {
			stream.Video = &video
		}
	}
	if metadata.Valid && metadata.String != "" {
		_ = json.Unmarshal([]byte(metadata.String), &stream.Metadata)
	}
	if rawJSON.Valid && rawJSON.String != "" {
		var radio RadioStation
		if err := json.Unmarshal([]byte(rawJSON.String), &radio); err == nil {
			stream.Radio = &radio
		}
	}
	if stream.Radio == nil && station.Valid && station.String != "" {
		stream.Radio = &RadioStation{
			StationUUID: station.String,
			Name:        stream.Name,
			URL:         stream.URL,
		}
	}
	if stream.Radio != nil {
		if stream.Radio.Name == "" {
			stream.Radio.Name = stream.Name
		}
		if stream.Radio.URL == "" {
			stream.Radio.URL = stream.URL
		}
	}
	return stream, nil
}

func streamArgs(stream StreamConfig) []any {
	created := stream.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	updated := stream.UpdatedAt
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	audioJSON := nullableJSON(stream.Audio)
	videoJSON := nullableJSON(stream.Video)
	metadataJSON := "{}"
	if stream.Metadata != nil {
		if data, err := json.Marshal(stream.Metadata); err == nil {
			metadataJSON = string(data)
		}
	}
	var rawJSON any
	if stream.Radio != nil {
		if data, err := json.Marshal(stream.Radio); err == nil {
			rawJSON = string(data)
		}
	}
	r := stream.Radio
	if r == nil {
		r = &RadioStation{}
	}
	if stream.Source == "" {
		stream.Source = "custom"
	}
	name := stream.Name
	if name == "" && r.Name != "" {
		name = r.Name
	}
	url := stream.URL
	if url == "" && r.URLResolved != "" {
		url = r.URLResolved
	}
	return []any{
		stream.ID, stream.Source, stream.Type, boolInt(stream.Enabled), stream.ChunkSeconds,
		audioJSON, videoJSON, metadataJSON,
		created.UTC().Format(time.RFC3339Nano), updated.UTC().Format(time.RFC3339Nano),
		nullableString(r.StationUUID), nullableString(r.ChangeUUID), nullableString(r.ServerUUID),
		name, url, nullableString(r.URLResolved), nullableString(r.Homepage), nullableString(r.Favicon),
		nullableString(r.Tags), nullableString(r.Country), nullableString(r.CountryCode),
		nullableString(r.State), nullableString(r.Language), nullableString(r.LanguageCodes),
		nullableInt(r.Votes), nullableString(r.LastChangeTime), nullableString(r.LastChangeTimeISO8601),
		nullableString(r.Codec), nullableInt(r.Bitrate), boolInt(r.IsHLS()), boolInt(r.IsHealthy()),
		nullableString(r.LastCheckTime), nullableString(r.LastCheckTimeISO8601),
		nullableString(r.LastCheckOKTime), nullableString(r.LastCheckOKTimeISO8601),
		nullableString(r.LastLocalCheckTime), nullableString(r.LastLocalCheckTimeISO8601),
		nullableString(r.ClickTimestamp), nullableString(r.ClickTimestampISO8601),
		nullableInt(r.ClickCount), nullableInt(r.ClickTrend), nullableInt(int(r.SSLError)),
		nullableFloat(r.GeoLat), nullableFloat(r.GeoLong), nullableFloat(r.GeoDistance),
		nullableInt(int(r.HasExtendedInfo)), nullableString(r.ISO31662), rawJSON,
	}
}

func nullableJSON(v any) any {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(data)
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullableFloat(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func isUniqueConstraint(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}
