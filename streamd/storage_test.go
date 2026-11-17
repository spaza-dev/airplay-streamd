package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "uploads.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, dir
}

func TestStoreLifecycle(t *testing.T) {
	store, dir := newTestStore(t)
	now := time.Now().UTC()

	local := filepath.Join(dir, "chunk.ogg")
	if err := os.WriteFile(local, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(QueueItem{
		StreamID: testID("s1"), LocalPath: local, ObjectKey: "s1/recordings/2026/01/01/120000.ogg",
		FileSize: 5, CreatedAt: now,
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	item, err := store.Claim(now)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if item == nil || item.ID != 1 || item.Status != "uploading" || item.Attempts != 1 {
		t.Fatalf("unexpected claim: %+v", item)
	}
	if again, err := store.Claim(now); err != nil || again != nil {
		t.Fatalf("second claim = %+v err=%v", again, err)
	}
	if err := store.Complete(item.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	counts, err := store.Counts()
	if err != nil || counts["completed"] != 1 {
		t.Fatalf("counts = %+v err=%v", counts, err)
	}

	missing := filepath.Join(dir, "second.ogg")
	if err := os.WriteFile(missing, []byte("more"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(QueueItem{StreamID: testID("s1"), LocalPath: missing, ObjectKey: "s1/recordings/second.ogg", FileSize: 4}); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Claim(time.Now().UTC())
	if err != nil || failed == nil {
		t.Fatalf("claim failed item: %+v err=%v", failed, err)
	}
	future := now.Add(time.Hour)
	if err := store.Fail(failed.ID, "temporary failure", &future); err != nil {
		t.Fatal(err)
	}
	if item, err := store.Claim(future.Add(-time.Minute)); err != nil || item != nil {
		t.Fatalf("claimed before retry time: %+v err=%v", item, err)
	}
	retried, err := store.Claim(future.Add(time.Second))
	if err != nil || retried == nil || retried.ID != failed.ID || retried.Attempts != 2 {
		t.Fatalf("retry claim = %+v err=%v", retried, err)
	}
	if err := store.Fail(retried.ID, "permanent", nil); err != nil {
		t.Fatal(err)
	}
	if item, err := store.Claim(future.Add(48 * time.Hour)); err != nil || item != nil {
		t.Fatalf("permanently failed item reclaimed: %+v err=%v", item, err)
	}

	items, err := store.List("completed", 10)
	if err != nil || len(items) != 1 || items[0].UploadedAt == nil {
		t.Fatalf("completed list = %+v err=%v", items, err)
	}
}

func TestStoreRecover(t *testing.T) {
	store, dir := newTestStore(t)
	now := time.Now().UTC()

	existing := filepath.Join(dir, "existing.ogg")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{existing, filepath.Join(dir, "gone.ogg")} {
		if err := store.Enqueue(QueueItem{StreamID: testID("s1"), LocalPath: path, ObjectKey: "k", FileSize: 1, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.Claim(now)
	if err != nil || first == nil {
		t.Fatalf("claim first: %+v err=%v", first, err)
	}
	second, err := store.Claim(now)
	if err != nil || second == nil {
		t.Fatalf("claim second: %+v err=%v", second, err)
	}
	if err := store.Recover(now); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	items, err := store.List("failed", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("failed items = %+v", items)
	}
	byPath := map[string]QueueItem{}
	for _, item := range items {
		byPath[item.LocalPath] = item
	}
	if got := byPath[existing]; got.NextAttemptAt == nil {
		t.Fatalf("existing file should be retryable: %+v", got)
	}
	if got := byPath[filepath.Join(dir, "gone.ogg")]; got.NextAttemptAt != nil || !strings.Contains(got.LastError, "missing") {
		t.Fatalf("missing file should be terminal: %+v", got)
	}
}

func TestHandleSegmentLineFinalizesAndEnqueues(t *testing.T) {
	dir := t.TempDir()
	cfg := validTestConfig(t, dir)
	logger, err := NewLogger("error", "")
	if err != nil {
		t.Fatal(err)
	}
	state := &RuntimeState{Stopped: map[string]bool{}}
	app := newApp(filepath.Join(dir, "config.yaml"), "", &cfg, logger, state, filepath.Join(dir, "runtime.json"))
	store, err := OpenStore(cfg.Uploader.QueueDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app.uploader = &Uploader{app: app, store: store, cfg: cfg.Uploader}

	eff := cfg.effectiveStream(StreamConfig{
		ID: testID("s1"), Name: "S1", Type: "direct", URL: "https://example.com/live.ogg", Enabled: true,
	})
	w := &Worker{app: app, stream: eff}

	incoming := filepath.Join(cfg.Recording.OutputDir, testID("s1"), ".incoming")
	if err := os.MkdirAll(incoming, 0o750); err != nil {
		t.Fatal(err)
	}
	writeChunk := func(name string, size int) string {
		path := filepath.Join(incoming, name)
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	seen := map[string]bool{}
	writeChunk("20260101120000.ogg", 42)
	w.handleSegmentLine(cfg, "20260101120000.ogg,0.000000,5.000000", seen)

	target := filepath.Join(cfg.Recording.OutputDir, testID("s1"), "2026-01-01", "120000.ogg")
	if info, err := os.Stat(target); err != nil || info.Size() != 42 {
		t.Fatalf("finalized chunk missing: %v", err)
	}
	items, err := store.List("pending", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("queued items = %+v err=%v", items, err)
	}
	if items[0].ObjectKey != testID("s1")+"/recordings/2026/01/01/120000.ogg" {
		t.Fatalf("object key = %q", items[0].ObjectKey)
	}

	w.handleSegmentLine(cfg, "20260101120000.ogg,0.000000,5.000000", seen)
	items, _ = store.List("pending", 10)
	if len(items) != 1 {
		t.Fatalf("duplicate list line enqueued twice: %+v", items)
	}

	writeChunk("20260101120000.ogg", 7)
	w.handleSegmentLine(cfg, "20260101120000.ogg,0.000000,5.000000", seen)
	second := filepath.Join(cfg.Recording.OutputDir, testID("s1"), "2026-01-01", "120000-1.ogg")
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("collision chunk missing: %v", err)
	}

	writeChunk("20260101120500.ogg", 0)
	w.handleSegmentLine(cfg, "20260101120500.ogg,0.000000,5.000000", seen)
	if _, err := os.Stat(filepath.Join(incoming, "20260101120500.ogg")); !os.IsNotExist(err) {
		t.Fatalf("empty chunk not removed: %v", err)
	}
}

type fakeS3 struct {
	mu       sync.Mutex
	objects  map[string][]byte
	types    map[string]string
	requests int
	fail     int
}

func newFakeS3(fail int) (*fakeS3, *httptest.Server) {
	f := &fakeS3{objects: map[string][]byte{}, types: map[string]string{}, fail: fail}
	return f, httptest.NewServer(f)
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	if f.fail > 0 {
		f.fail--
		f.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	f.mu.Unlock()

	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.objects[r.URL.Path] = body
	f.types[r.URL.Path] = r.Header.Get("Content-Type")
	f.mu.Unlock()
	w.Header().Set("ETag", `"fake-etag"`)
	w.WriteHeader(http.StatusOK)
}

func (f *fakeS3) get(path string) ([]byte, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, ok := f.objects[path]
	return body, f.types[path], ok
}

func newUploaderTestApp(t *testing.T, dir, endpoint string) (*App, Config) {
	t.Helper()
	cfg := validTestConfig(t, dir)
	cfg.Uploader.Enabled = true
	cfg.Uploader.Concurrency = 2
	cfg.Uploader.Retry = RetryConfig{InitialIntervalSeconds: 1, MaxIntervalSeconds: 2, MaxAttempts: 3}
	cfg.Uploader.CleanupAfterUpload = true
	cfg.Storage = StorageConfig{
		Endpoint: endpoint, Region: "auto", Bucket: "test-bucket",
		AccessKeyID: "key", SecretAccessKey: "secret", ForcePathStyle: true,
	}
	logger, err := NewLogger("error", "")
	if err != nil {
		t.Fatal(err)
	}
	state := &RuntimeState{Stopped: map[string]bool{}}
	app := newApp(filepath.Join(dir, "config.yaml"), "", &cfg, logger, state, filepath.Join(dir, "runtime.json"))
	return app, cfg
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestUploaderUploadsAndCleansUp(t *testing.T) {
	fake, srv := newFakeS3(0)
	defer srv.Close()
	dir := t.TempDir()
	app, cfg := newUploaderTestApp(t, dir, srv.URL)

	up, err := NewUploader(app)
	if err != nil {
		t.Fatalf("NewUploader: %v", err)
	}
	app.uploader = up
	up.Start()
	defer up.Stop()

	local := filepath.Join(dir, "chunk.ogg")
	if err := os.WriteFile(local, []byte("chunk-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := up.Enqueue(QueueItem{
		StreamID: testID("s1"), LocalPath: local, ObjectKey: "s1/recordings/2026/01/01/120000.ogg",
		FileSize: 11, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	waitFor(t, 10*time.Second, func() bool {
		items, err := up.Queue("completed", 10)
		return err == nil && len(items) == 1
	})
	body, ctype, ok := fake.get("/test-bucket/s1/recordings/2026/01/01/120000.ogg")
	if !ok || string(body) != "chunk-bytes" {
		t.Fatalf("uploaded object = %q ok=%v", body, ok)
	}
	if ctype != "audio/ogg" {
		t.Fatalf("content type = %q", ctype)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("local chunk not cleaned up: %v", err)
	}

	eff := cfg.effectiveStream(StreamConfig{
		ID: testID("s1"), Name: "S1", Type: "direct", URL: "https://example.com/live.ogg", Enabled: true,
		Metadata: map[string]any{"country": "NL"},
	})
	up.PublishMeta(eff)
	waitFor(t, 10*time.Second, func() bool {
		_, _, ok := fake.get("/test-bucket/" + testID("s1") + "/meta.json")
		return ok
	})
	metaBody, metaType, _ := fake.get("/test-bucket/" + testID("s1") + "/meta.json")
	if metaType != "application/json" {
		t.Fatalf("meta content type = %q", metaType)
	}
	var doc map[string]any
	if err := json.Unmarshal(metaBody, &doc); err != nil {
		t.Fatalf("meta JSON: %v", err)
	}
	if doc["id"] != testID("s1") || doc["name"] != "S1" || doc["format"] != "ogg" {
		t.Fatalf("meta doc = %+v", doc)
	}
	if _, exists := doc["url"]; exists {
		t.Fatalf("meta doc must not contain url: %+v", doc)
	}
	if doc["created_at"] == "" || doc["updated_at"] == "" {
		t.Fatalf("meta timestamps missing: %+v", doc)
	}
	md, _ := doc["metadata"].(map[string]any)
	if md["country"] != "NL" {
		t.Fatalf("meta metadata = %+v", doc["metadata"])
	}
}

func TestStoreConcurrentEnqueueAndClaim(t *testing.T) {
	store, dir := newTestStore(t)
	local := filepath.Join(dir, "chunk.ogg")
	if err := os.WriteFile(local, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 200)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.Enqueue(QueueItem{
				StreamID: testID("s1"), LocalPath: local, ObjectKey: "k", FileSize: 1,
				CreatedAt: time.Now().UTC(),
			}); err != nil {
				errs <- err
			}
		}()
	}
	stop := make(chan struct{})
	var claimWG sync.WaitGroup
	claimWG.Add(1)
	go func() {
		defer claimWG.Done()
		for {
			item, err := store.Claim(time.Now().UTC())
			if err != nil {
				errs <- err
				return
			}
			if item != nil {
				if err := store.Complete(item.ID); err != nil {
					errs <- err
					return
				}
				continue
			}
			select {
			case <-stop:
				return
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}()

	wg.Wait()
	close(stop)
	claimWG.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent queue operation: %v", err)
	}
	counts, err := store.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if counts["completed"] != 100 {
		t.Fatalf("completed = %d, want 100", counts["completed"])
	}
}
