package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Scope struct {
	Streams         StreamScope `json:"streams"`
	Dates           DateScope   `json:"dates"`
	DailyTimeWindow *TimeWindow `json:"daily_time_window,omitempty"`
}

type StreamScope struct {
	Mode    string   `json:"mode"`
	IDs     []string `json:"ids,omitempty"`
	Country string   `json:"country,omitempty"`
}

type DateScope struct {
	Mode string `json:"mode"`
	Date string `json:"date,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type TimeWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Basis string `json:"basis,omitempty"`
	Match string `json:"match,omitempty"`
}

func ParseScope(raw string) (*Scope, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("scope is required")
	}
	var scope Scope
	if err := json.Unmarshal([]byte(raw), &scope); err != nil {
		return nil, fmt.Errorf("invalid scope JSON: %w", err)
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return &scope, nil
}

func (s *Scope) Validate() error {
	s.Streams.Mode = strings.ToLower(strings.TrimSpace(s.Streams.Mode))
	switch s.Streams.Mode {
	case "all":
	case "ids":
		if len(s.Streams.IDs) == 0 {
			return fmt.Errorf("streams.ids must not be empty when mode is ids")
		}
		for _, id := range s.Streams.IDs {
			if !streamIDPattern.MatchString(id) {
				return fmt.Errorf("invalid stream id %q", id)
			}
		}
	case "country":
		if strings.TrimSpace(s.Streams.Country) == "" {
			return fmt.Errorf("streams.country is required when mode is country")
		}
	default:
		return fmt.Errorf("streams.mode must be all, ids, or country")
	}

	s.Dates.Mode = strings.ToLower(strings.TrimSpace(s.Dates.Mode))
	switch s.Dates.Mode {
	case "date":
		s.Dates.Date = strings.TrimSpace(s.Dates.Date)
		if _, err := time.Parse("2006-01-02", s.Dates.Date); err != nil {
			return fmt.Errorf("dates.date must be YYYY-MM-DD")
		}
	case "range":
		s.Dates.From = strings.TrimSpace(s.Dates.From)
		s.Dates.To = strings.TrimSpace(s.Dates.To)
		from, err := time.Parse("2006-01-02", s.Dates.From)
		if err != nil {
			return fmt.Errorf("dates.from must be YYYY-MM-DD")
		}
		to, err := time.Parse("2006-01-02", s.Dates.To)
		if err != nil {
			return fmt.Errorf("dates.to must be YYYY-MM-DD")
		}
		if to.Before(from) {
			return fmt.Errorf("dates.to must be on or after dates.from")
		}
		if to.Sub(from) > 366*24*time.Hour {
			return fmt.Errorf("date range must not exceed 366 days")
		}
	default:
		return fmt.Errorf("dates.mode must be date or range")
	}

	if s.DailyTimeWindow != nil {
		tw := s.DailyTimeWindow
		tw.Basis = strings.ToLower(strings.TrimSpace(tw.Basis))
		tw.Match = strings.ToLower(strings.TrimSpace(tw.Match))
		if tw.Basis == "" {
			tw.Basis = "stream_local"
		}
		if tw.Basis != "stream_local" {
			return fmt.Errorf("daily_time_window.basis must be stream_local")
		}
		if tw.Match == "" {
			tw.Match = "overlap"
		}
		if tw.Match != "overlap" {
			return fmt.Errorf("daily_time_window.match must be overlap")
		}
		if _, err := clockMinutes(tw.Start); err != nil {
			return fmt.Errorf("daily_time_window.start must be HH:MM")
		}
		if _, err := clockMinutes(tw.End); err != nil {
			return fmt.Errorf("daily_time_window.end must be HH:MM")
		}
	}
	return nil
}

func (s *Scope) DateFrom() string {
	if s.Dates.Mode == "date" {
		return s.Dates.Date
	}
	return s.Dates.From
}

func (s *Scope) DateTo() string {
	if s.Dates.Mode == "date" {
		return s.Dates.Date
	}
	return s.Dates.To
}

func (s *Scope) IndexDates() ([]string, error) {
	from, err := time.Parse("2006-01-02", s.DateFrom())
	if err != nil {
		return nil, err
	}
	to, err := time.Parse("2006-01-02", s.DateTo())
	if err != nil {
		return nil, err
	}
	from = from.AddDate(0, 0, -1)
	var out []string
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		out = append(out, day.Format("2006-01-02"))
	}
	return out, nil
}

func (s *Scope) AllowsOccurrence(occ time.Time, duration time.Duration, timezone string) bool {
	loc, err := time.LoadLocation(timezone)
	if err != nil || timezone == "" {
		loc = time.UTC
	}
	local := occ.In(loc)
	date := local.Format("2006-01-02")
	windowDay := date
	startMin, endMin := 0, 0
	crosses := false
	if s.DailyTimeWindow != nil {
		startMin, _ = clockMinutes(s.DailyTimeWindow.Start)
		endMin, _ = clockMinutes(s.DailyTimeWindow.End)
		crosses = endMin < startMin
		if crosses {
			minutes := local.Hour()*60 + local.Minute()
			if minutes < endMin {
				windowDay = local.AddDate(0, 0, -1).Format("2006-01-02")
			}
		}
	}
	if s.Dates.Mode == "date" {
		if windowDay != s.Dates.Date {
			return false
		}
	} else if windowDay < s.Dates.From || windowDay > s.Dates.To {
		return false
	}
	if s.DailyTimeWindow == nil || startMin == endMin {
		return true
	}
	if duration <= 0 {
		duration = time.Millisecond
	}
	years, months, days := local.Date()
	candidates := []time.Time{time.Date(years, months, days, 0, 0, 0, 0, loc)}
	if crosses {
		candidates = append(candidates, time.Date(years, months, days, 0, 0, 0, 0, loc).AddDate(0, 0, -1))
	}
	for _, day := range candidates {
		start := day.Add(time.Duration(startMin) * time.Minute)
		var end time.Time
		if crosses {
			end = day.Add(24*time.Hour + time.Duration(endMin)*time.Minute)
		} else {
			end = day.Add(time.Duration(endMin) * time.Minute)
		}
		if occ.Before(end) && occ.Add(duration).After(start) {
			return true
		}
	}
	return false
}

func clockMinutes(value string) (int, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}
