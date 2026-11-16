package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type Logger struct {
	mu      sync.RWMutex
	level   int
	entries []LogEntry
	max     int
	file    *os.File
}

var logLevels = map[string]int{"debug": 0, "info": 1, "warn": 2, "warning": 2, "error": 3}

func NewLogger(level, filePath string) (*Logger, error) {
	l := &Logger{level: logLevels[strings.ToLower(level)], max: 5000}
	if l.level == 0 && strings.ToLower(level) != "debug" {
		l.level = 1
	}
	if filePath != "" {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o750); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			return nil, err
		}
		l.file = f
	}
	return l, nil
}

func (l *Logger) Close() {
	if l.file != nil {
		_ = l.file.Close()
	}
}

func (l *Logger) log(level, format string, args ...any) {
	if logLevels[level] < l.level {
		return
	}
	message := fmt.Sprintf(format, args...)
	entry := LogEntry{Time: time.Now().UTC(), Level: level, Message: message}
	l.mu.Lock()
	l.entries = append(l.entries, entry)
	if len(l.entries) > l.max {
		l.entries = append([]LogEntry(nil), l.entries[len(l.entries)-l.max:]...)
	}
	file := l.file
	l.mu.Unlock()
	line := fmt.Sprintf("%s %-5s %s\n", entry.Time.Format(time.RFC3339), strings.ToUpper(level), message)
	_, _ = io.WriteString(os.Stderr, line)
	if file != nil {
		_, _ = io.WriteString(file, line)
	}
}

func (l *Logger) Debugf(format string, args ...any) { l.log("debug", format, args...) }
func (l *Logger) Infof(format string, args ...any)  { l.log("info", format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.log("warn", format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.log("error", format, args...) }

func (l *Logger) Recent(limit int) []LogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if limit <= 0 || limit > len(l.entries) {
		limit = len(l.entries)
	}
	out := make([]LogEntry, limit)
	copy(out, l.entries[len(l.entries)-limit:])
	return out
}
