package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestStreamStoreCustomLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "streams.db")
	store, err := OpenStreamStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	stream := StreamConfig{
		ID:      uuid.NewString(),
		Source:  "custom",
		Name:    "Test Radio",
		Type:    "radio",
		URL:     "https://example.com/live.ogg",
		Enabled: true,
		Audio: &AudioConfig{
			Codec: "libopus", Container: "ogg", Bitrate: "96k",
			SampleRate: 48000, Channels: 2,
		},
		Metadata: map[string]any{"country": "Kenya"},
	}
	if err := store.Create(ctx, stream); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, ok, err := store.Get(ctx, stream.ID)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Name != stream.Name || got.Source != "custom" || !got.Enabled {
		t.Fatalf("unexpected stream: %+v", got)
	}
	if got.Audio == nil || got.Audio.Codec != "libopus" || got.Audio.Bitrate != "96k" {
		t.Fatalf("audio not restored: %+v", got.Audio)
	}
	if got.Metadata["country"] != "Kenya" {
		t.Fatalf("metadata not restored: %+v", got.Metadata)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("timestamps missing: %+v", got)
	}

	got.Name = "Renamed"
	got.Enabled = false
	if err := store.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	updated, _, err := store.Get(ctx, stream.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Renamed" || updated.Enabled {
		t.Fatalf("update not applied: %+v", updated)
	}

	list, err := store.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v err=%v", list, err)
	}

	if err := store.Delete(ctx, stream.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, err := store.Get(ctx, stream.ID); err != nil || ok {
		t.Fatalf("stream still present: ok=%v err=%v", ok, err)
	}
	if err := store.Delete(ctx, stream.ID); err != ErrStreamNotFound {
		t.Fatalf("delete missing = %v", err)
	}
}

func TestStreamStoreRadioStation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "streams.db")
	store, err := OpenStreamStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	stationUUID := uuid.NewString()
	stream := StreamConfig{
		ID:      stationUUID,
		Source:  "radio-browser",
		Name:    "Capital FM",
		Type:    "radio",
		URL:     "https://example.com/capital",
		Enabled: true,
		Radio: &RadioStation{
			StationUUID: stationUUID,
			Name:        "Capital FM",
			URL:         "https://example.com/capital",
			URLResolved: "https://example.com/capital.mp3",
			Country:     "Kenya",
			CountryCode: "KE",
			Codec:       "MP3",
			Bitrate:     128,
			HLS:         flexInt(0),
			LastCheckOK: flexInt(1),
		},
		Metadata: map[string]any{"source": "radio-browser"},
	}
	if err := store.Create(context.Background(), stream); err != nil {
		t.Fatalf("create radio: %v", err)
	}
	got, ok, err := store.GetByStationUUID(context.Background(), stationUUID)
	if err != nil || !ok {
		t.Fatalf("get by station uuid: ok=%v err=%v", ok, err)
	}
	if got.ID != stationUUID || got.Source != "radio-browser" || got.Radio == nil {
		t.Fatalf("unexpected radio stream: %+v", got)
	}
	if got.Radio.Country != "Kenya" || got.Radio.Bitrate != 128 || !got.Radio.IsHealthy() {
		t.Fatalf("radio not restored: %+v", got.Radio)
	}

	customID := uuid.NewString()
	bad := stream
	bad.ID = customID
	bad.Radio = nil
	if err := store.Create(context.Background(), bad); err == nil {
		t.Fatal("radio stream with mismatched stationuuid was accepted")
	}

	stationIDs, err := store.StationUUIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stationIDs[stationUUID] != stationUUID {
		t.Fatalf("station ids = %+v", stationIDs)
	}
}

func TestStreamStorePersistsAndMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "streams.db")
	store, err := OpenStreamStore(path)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if err := store.Create(context.Background(), StreamConfig{
		ID: id, Source: "custom", Name: "Persisted", Type: "direct",
		URL: "https://example.com/a.ogg",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStreamStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()
	got, ok, err := store.Get(context.Background(), id)
	if err != nil || !ok || got.Name != "Persisted" {
		t.Fatalf("stream not persisted: %+v ok=%v err=%v", got, ok, err)
	}
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("schema version = %d", version)
	}
}
