package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	_ "modernc.org/sqlite"
)

type QueueItem struct {
	ID            int64
	StreamID      string
	LocalPath     string
	ObjectKey     string
	FileSize      int64
	Status        string
	Attempts      int
	NextAttemptAt *time.Time
	LastError     string
	CreatedAt     time.Time
	UploadedAt    *time.Time
}

type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	schema := `
CREATE TABLE IF NOT EXISTS uploads (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    stream_id TEXT NOT NULL,
    local_path TEXT NOT NULL,
    object_key TEXT NOT NULL,
    file_size INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    uploaded_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_uploads_status_next_attempt
    ON uploads(status, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_uploads_stream
    ON uploads(stream_id);
`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Enqueue(item QueueItem) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := item.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO uploads (stream_id, local_path, object_key, file_size, status, attempts, next_attempt_at, created_at)
		 VALUES (?, ?, ?, ?, 'pending', 0, ?, ?)`,
		item.StreamID, item.LocalPath, item.ObjectKey, item.FileSize,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) Claim(now time.Time) (*QueueItem, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	nowStr := now.UTC().Format(time.RFC3339Nano)
	row := tx.QueryRow(
		`SELECT id, stream_id, local_path, object_key, file_size, status, attempts,
		        next_attempt_at, last_error, created_at, uploaded_at
		 FROM uploads
		 WHERE status = 'pending'
		    OR (status = 'failed' AND next_attempt_at IS NOT NULL AND next_attempt_at <= ?)
		 ORDER BY id LIMIT 1`, nowStr)
	item, err := scanQueueItem(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(`UPDATE uploads SET status = 'uploading', attempts = attempts + 1 WHERE id = ? AND status = ?`, item.ID, item.Status)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, fmt.Errorf("upload claim conflict for id %d", item.ID)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	item.Status = "uploading"
	item.Attempts++
	return item, nil
}

func (s *Store) Complete(id int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE uploads SET status = 'completed', uploaded_at = ?, last_error = '' WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) Fail(id int64, errMsg string, next *time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.failLocked(id, errMsg, next)
}

func (s *Store) failLocked(id int64, errMsg string, next *time.Time) error {
	var nextVal any
	if next != nil {
		nextVal = next.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`UPDATE uploads SET status = 'failed', last_error = ?, next_attempt_at = ? WHERE id = ?`, errMsg, nextVal, id)
	return err
}

func (s *Store) Counts() (map[string]int, error) {
	counts := map[string]int{"pending": 0, "uploading": 0, "failed": 0, "completed": 0}
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM uploads GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		counts[status] = n
	}
	return counts, rows.Err()
}

func (s *Store) BacklogBytes() (int64, error) {
	var total int64
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(file_size), 0) FROM uploads WHERE status IN ('pending', 'uploading', 'failed')`,
	).Scan(&total)
	return total, err
}

func (s *Store) List(status string, limit int) ([]QueueItem, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := `SELECT id, stream_id, local_path, object_key, file_size, status, attempts,
	                 next_attempt_at, last_error, created_at, uploaded_at
	          FROM uploads`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]QueueItem, 0, limit)
	for rows.Next() {
		item, err := scanQueueItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (s *Store) Recover(now time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	nowStr := now.UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(
		`UPDATE uploads SET status = 'failed', next_attempt_at = ?, last_error = 'recovered interrupted upload'
		 WHERE status = 'uploading'`, nowStr); err != nil {
		return err
	}

	rows, err := s.db.Query(
		`SELECT id, local_path FROM uploads
		 WHERE status IN ('pending', 'failed') AND (next_attempt_at IS NOT NULL OR status = 'pending')`)
	if err != nil {
		return err
	}
	type missing struct {
		id   int64
		path string
	}
	var missingItems []missing
	for rows.Next() {
		var m missing
		if err := rows.Scan(&m.id, &m.path); err != nil {
			rows.Close()
			return err
		}
		if _, err := os.Stat(m.path); err != nil {
			missingItems = append(missingItems, m)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, m := range missingItems {
		_ = s.failLocked(m.id, "local file missing: "+filepath.Base(m.path), nil)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanQueueItem(row rowScanner) (*QueueItem, error) {
	var (
		item      QueueItem
		nextStr   sql.NullString
		uploadStr sql.NullString
		lastError sql.NullString
		created   string
	)
	if err := row.Scan(&item.ID, &item.StreamID, &item.LocalPath, &item.ObjectKey, &item.FileSize,
		&item.Status, &item.Attempts, &nextStr, &lastError, &created, &uploadStr); err != nil {
		return nil, err
	}
	if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
		item.CreatedAt = t
	}
	if nextStr.Valid && nextStr.String != "" {
		if t, err := time.Parse(time.RFC3339Nano, nextStr.String); err == nil {
			item.NextAttemptAt = &t
		}
	}
	if uploadStr.Valid && uploadStr.String != "" {
		if t, err := time.Parse(time.RFC3339Nano, uploadStr.String); err == nil {
			item.UploadedAt = &t
		}
	}
	item.LastError = lastError.String
	return &item, nil
}

// ---------------------------------------------------------------------------
// Uploader
// ---------------------------------------------------------------------------

type UploaderStatus struct {
	Enabled     bool           `json:"enabled"`
	Concurrency int            `json:"concurrency"`
	Active      int            `json:"active"`
	Counts      map[string]int `json:"counts"`
}

type Uploader struct {
	app     *App
	store   *Store
	client  *s3.Client
	cfg     UploaderConfig
	storage StorageConfig
	wake    chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	claimMu sync.Mutex
	mu      sync.Mutex
	active  int
}

func NewUploader(app *App) (*Uploader, error) {
	cfg := app.Config()
	store, err := OpenStore(cfg.Uploader.QueueDB)
	if err != nil {
		return nil, fmt.Errorf("open upload queue: %w", err)
	}
	client, err := newS3Client(context.Background(), cfg.Storage)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("create storage client: %w", err)
	}
	return &Uploader{
		app: app, store: store, client: client,
		cfg: cfg.Uploader, storage: cfg.Storage,
		wake: make(chan struct{}, 1),
	}, nil
}

func newS3Client(ctx context.Context, st StorageConfig) (*s3.Client, error) {
	region := strings.TrimSpace(st.Region)
	if region == "" {
		region = "auto"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(st.AccessKeyID, st.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, err
	}
	cfg.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	cfg.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		if st.Endpoint != "" {
			o.BaseEndpoint = aws.String(st.Endpoint)
		}
		o.UsePathStyle = st.ForcePathStyle
	}), nil
}

func (u *Uploader) Start() {
	u.ctx, u.cancel = context.WithCancel(u.app.ctx)
	if err := u.store.Recover(time.Now().UTC()); err != nil {
		u.app.logger.Errorf("", "upload queue recovery: %v", err)
	}
	if counts, err := u.store.Counts(); err == nil {
		u.app.logger.Infof("", "uploader started: concurrency=%d pending=%d failed=%d completed=%d",
			u.cfg.Concurrency, counts["pending"], counts["failed"], counts["completed"])
	}
	for i := 0; i < u.cfg.Concurrency; i++ {
		u.wg.Add(1)
		go u.worker()
	}
}

func (u *Uploader) Stop() {
	if u.cancel != nil {
		u.cancel()
	}
	u.wg.Wait()
	_ = u.store.Close()
}

func (u *Uploader) notify() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

func (u *Uploader) Enqueue(item QueueItem) error {
	if err := u.store.Enqueue(item); err != nil {
		return err
	}
	u.notify()
	return nil
}

func (u *Uploader) worker() {
	defer u.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-u.ctx.Done():
			return
		case <-u.wake:
		case <-ticker.C:
		}
		for {
			if u.ctx.Err() != nil {
				return
			}
			item, err := u.claim()
			if err != nil {
				u.app.logger.Errorf("", "claim upload: %v", err)
				break
			}
			if item == nil {
				break
			}
			u.process(item)
		}
	}
}

func (u *Uploader) claim() (*QueueItem, error) {
	u.claimMu.Lock()
	defer u.claimMu.Unlock()
	return u.store.Claim(time.Now().UTC())
}

func (u *Uploader) process(item *QueueItem) {
	u.mu.Lock()
	u.active++
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.active--
		u.mu.Unlock()
	}()

	if err := u.upload(item); err != nil {
		next, retry := u.retryAt(item.Attempts)
		if retry {
			if ferr := u.store.Fail(item.ID, err.Error(), &next); ferr != nil {
				u.app.logger.Errorf(item.StreamID, "record upload retry: %v", ferr)
			}
			u.app.logger.Warnf(item.StreamID, "upload failed (attempt %d), retry at %s: %v",
				item.Attempts, next.Format(time.RFC3339), err)
		} else {
			if ferr := u.store.Fail(item.ID, err.Error(), nil); ferr != nil {
				u.app.logger.Errorf(item.StreamID, "record upload failure: %v", ferr)
			}
			u.app.logger.Errorf(item.StreamID, "upload failed permanently after %d attempts: %v", item.Attempts, err)
		}
		return
	}

	if err := u.store.Complete(item.ID); err != nil {
		u.app.logger.Errorf(item.StreamID, "mark upload complete: %v", err)
	}
	u.app.logger.Infof(item.StreamID, "uploaded %s (%d bytes)", item.ObjectKey, item.FileSize)
	if u.cfg.CleanupAfterUpload {
		if err := os.Remove(item.LocalPath); err != nil && !os.IsNotExist(err) {
			u.app.logger.Warnf(item.StreamID, "remove local chunk: %v", err)
		}
	}
}

func (u *Uploader) retryAt(attempts int) (time.Time, bool) {
	if u.cfg.Retry.MaxAttempts > 0 && attempts >= u.cfg.Retry.MaxAttempts {
		return time.Time{}, false
	}
	initial := time.Duration(u.cfg.Retry.InitialIntervalSeconds) * time.Second
	maxDelay := time.Duration(u.cfg.Retry.MaxIntervalSeconds) * time.Second
	delay := initial
	for i := 1; i < attempts; i++ {
		if delay >= maxDelay/2 {
			delay = maxDelay
			break
		}
		delay *= 2
	}
	if delay > maxDelay {
		delay = maxDelay
	}
	return time.Now().Add(delay), true
}

func (u *Uploader) upload(item *QueueItem) error {
	f, err := os.Open(item.LocalPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(u.ctx, 30*time.Minute)
	defer cancel()
	_, err = u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(u.storage.Bucket),
		Key:           aws.String(item.ObjectKey),
		Body:          f,
		ContentLength: aws.Int64(info.Size()),
		ContentType:   aws.String(contentTypeFor(item.ObjectKey)),
	})
	return err
}

func (u *Uploader) Status() (UploaderStatus, error) {
	counts, err := u.store.Counts()
	if err != nil {
		return UploaderStatus{}, err
	}
	u.mu.Lock()
	active := u.active
	u.mu.Unlock()
	return UploaderStatus{
		Enabled:     u.cfg.Enabled,
		Concurrency: u.cfg.Concurrency,
		Active:      active,
		Counts:      counts,
	}, nil
}

func (u *Uploader) Queue(status string, limit int) ([]QueueItem, error) {
	return u.store.List(status, limit)
}

func (u *Uploader) BacklogBytes() (int64, error) {
	if u == nil || u.store == nil {
		return 0, nil
	}
	return u.store.BacklogBytes()
}

func (u *Uploader) PublishMeta(stream StreamConfig) {
	if u == nil || u.client == nil {
		return
	}
	if !u.cfg.Enabled {
		return
	}
	go func() {
		created, err := u.app.metaCreatedAt(stream.ID)
		if err != nil {
			u.app.logger.Warnf(stream.ID, "persist metadata timestamp: %v", err)
			created = time.Now().UTC()
		}
		cfg := u.app.Config()
		doc := map[string]any{
			"id":         stream.ID,
			"name":       stream.Name,
			"type":       stream.Type,
			"format":     cfg.streamFormat(stream),
			"created_at": created.UTC().Format(time.RFC3339),
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		}
		metadata := stream.Metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		doc["metadata"] = metadata
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			u.app.logger.Errorf(stream.ID, "encode metadata: %v", err)
			return
		}

		initial := time.Duration(u.cfg.Retry.InitialIntervalSeconds) * time.Second
		maxDelay := time.Duration(u.cfg.Retry.MaxIntervalSeconds) * time.Second
		delay := initial
		key := stream.ID + "/meta.json"
		for {
			if u.ctx.Err() != nil {
				return
			}
			ctx, cancel := context.WithTimeout(u.ctx, 30*time.Second)
			_, err := u.client.PutObject(ctx, &s3.PutObjectInput{
				Bucket:        aws.String(u.storage.Bucket),
				Key:           aws.String(key),
				Body:          bytes.NewReader(data),
				ContentLength: aws.Int64(int64(len(data))),
				ContentType:   aws.String("application/json"),
			})
			cancel()
			if err == nil {
				u.app.logger.Infof(stream.ID, "published metadata %s", key)
				return
			}
			u.app.logger.Warnf(stream.ID, "publish metadata failed, retrying in %s: %v", delay, err)
			if !sleepContext(u.ctx, delay) {
				return
			}
			delay = growDelay(delay, maxDelay)
		}
	}()
}

func contentTypeFor(key string) string {
	switch strings.ToLower(filepath.Ext(key)) {
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".webm":
		return "video/webm"
	case ".mkv":
		return "video/x-matroska"
	case ".json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}
