package main

import (
	"database/sql"
	"time"
)

type Job struct {
	ID               int64      `json:"id"`
	Status           string     `json:"status"`
	Stage            string     `json:"stage"`
	Progress         float64    `json:"progress"`
	Error            string     `json:"error"`
	ScopeJSON        string     `json:"scope"`
	CallbackURL      string     `json:"callback_url"`
	RequestedBy      string     `json:"requested_by"`
	TotalFiles       int        `json:"total_files"`
	TotalIndexes     int        `json:"total_indexes"`
	ProcessedIndexes int        `json:"processed_indexes"`
	TotalMatches     int        `json:"total_matches"`
	Attempts         int        `json:"attempts"`
	NextAttemptAt    *time.Time `json:"next_attempt_at,omitempty"`
	CancelRequested  bool       `json:"cancel_requested"`
	PauseRequested   bool       `json:"pause_requested"`
	CreatedAt        time.Time  `json:"created_at"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type JobFile struct {
	ID           int64     `json:"id"`
	JobID        int64     `json:"job_id"`
	OriginalName string    `json:"original_name"`
	StoredName   string    `json:"stored_name"`
	Path         string    `json:"-"`
	SourceURL    string    `json:"source_url"`
	SizeBytes    int64     `json:"size_bytes"`
	ContentType  string    `json:"content_type"`
	CreatedAt    time.Time `json:"created_at"`
}

type JobIndex struct {
	ID          int64      `json:"id"`
	JobID       int64      `json:"job_id"`
	StreamID    string     `json:"stream_id"`
	Date        string     `json:"date"`
	BuildID     string     `json:"build_id"`
	BuildPrefix string     `json:"build_prefix"`
	TimelineKey string     `json:"timeline_key"`
	DataKey     string     `json:"data_key"`
	LockKey     string     `json:"lock_key"`
	Timezone    string     `json:"timezone"`
	Status      string     `json:"status"`
	SizeBytes   int64      `json:"size_bytes"`
	Matches     int        `json:"matches"`
	Error       string     `json:"error"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type Match struct {
	ID               int64     `json:"id"`
	JobID            int64     `json:"job_id"`
	JobIndexID       int64     `json:"job_index_id"`
	FileID           int64     `json:"file_id"`
	FileName         string    `json:"file_name"`
	QueryPath        string    `json:"query_path"`
	ResourceID       string    `json:"resource_id"`
	StreamID         string    `json:"stream_id"`
	Date             string    `json:"date"`
	RecordingStart   time.Time `json:"recording_start_time"`
	Occurrence       time.Time `json:"occurrence_time"`
	ReferenceStartMS int64     `json:"reference_start_ms"`
	ReferenceStopMS  int64     `json:"reference_stop_ms"`
	MatchCount       int       `json:"match_count"`
	Score            float64   `json:"score"`
	TimelineStartMS  int64     `json:"timeline_start_ms"`
	TimelineEndMS    int64     `json:"timeline_end_ms"`
	CreatedAt        time.Time `json:"created_at"`
}

type JobLog struct {
	ID        int64     `json:"id"`
	JobID     int64     `json:"job_id"`
	Level     string    `json:"level"`
	Stage     string    `json:"stage"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type CallbackDelivery struct {
	ID           int64     `json:"id"`
	JobID        int64     `json:"job_id"`
	Event        string    `json:"event"`
	URL          string    `json:"url"`
	StatusCode   int       `json:"status_code"`
	Attempt      int       `json:"attempt"`
	Success      bool      `json:"success"`
	Error        string    `json:"error"`
	ResponseBody string    `json:"response_body"`
	CreatedAt    time.Time `json:"created_at"`
}

func parseDBTime(value string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, value)
	return t.UTC()
}

func nullableTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func timeValue(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func utcNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }
