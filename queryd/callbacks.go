package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type CallbackEnvelope struct {
	Event     string    `json:"event"`
	JobID     int64     `json:"job_id"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data,omitempty"`
}

func (a *App) logJob(jobID int64, level, stage, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	a.db.AddJobLog(jobID, level, stage, message)
	a.logger.log(level, "job %d %s: %s", jobID, stage, message)
}

func (a *App) deliverCallback(ctx context.Context, job *Job, event string, data any) {
	if job == nil || strings.TrimSpace(job.CallbackURL) == "" {
		return
	}
	payload := CallbackEnvelope{
		Event:     event,
		JobID:     job.ID,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		a.logJob(job.ID, "error", "callback", "encode %s callback: %v", event, err)
		return
	}
	client := &http.Client{Timeout: time.Duration(a.cfg.Callbacks.TimeoutSeconds) * time.Second}
	maxAttempts := a.cfg.Callbacks.MaxAttempts
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.CallbackURL, bytes.NewReader(body))
		if err != nil {
			a.db.AddCallbackDelivery(ctx, job.ID, event, job.CallbackURL, 0, attempt, false, err.Error(), string(body), "")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "queryd/"+version)
		req.Header.Set("X-Queryd-Event", event)
		if secret := a.cfg.Callbacks.SigningSecret; secret != "" {
			mac := hmac.New(sha256.New, []byte(secret))
			_, _ = mac.Write(body)
			req.Header.Set("X-Queryd-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
		resp, err := client.Do(req)
		status := 0
		responseBody := ""
		if resp != nil {
			status = resp.StatusCode
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			responseBody = string(data)
			_ = resp.Body.Close()
		}
		success := err == nil && status >= 200 && status < 300
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		} else if !success {
			errMsg = fmt.Sprintf("callback returned HTTP %d", status)
		}
		a.db.AddCallbackDelivery(ctx, job.ID, event, job.CallbackURL, status, attempt, success, errMsg, string(body), responseBody)
		if success {
			a.logJob(job.ID, "info", "callback", "delivered %s callback (attempt %d)", event, attempt)
			return
		}
		a.logJob(job.ID, "warn", "callback", "deliver %s callback attempt %d: %s", event, attempt, errMsg)
		if attempt == maxAttempts {
			return
		}
		delay := time.Duration(1<<uint(attempt-1)) * time.Second
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
