package model

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseSendAt turns what a user typed into a fire time. It accepts relative
// offsets, a bare clock time, and full timestamps, because the useful question in
// a TUI is "in 20 minutes" not "what is RFC3339 again".
func ParseSendAt(now time.Time, raw string) (*time.Time, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(strings.ToLower(s), "in ")
	s = strings.TrimPrefix(s, "at ")
	if s == "" {
		return nil, fmt.Errorf("no time given")
	}

	if d, err := parseDurationLoose(s); err == nil && d > 0 {
		at := now.Add(d)
		return &at, nil
	}

	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-1-2 15:04",
		"2006-01-02",
		"01-02 15:04",
		"1/2 15:04",
		"15:04:05",
		"15:04",
	}
	for _, layout := range layouts {
		t, err := time.ParseInLocation(layout, s, now.Location())
		if err != nil {
			continue
		}
		switch {
		case layout == "15:04" || layout == "15:04:05":
			t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, now.Location())
			if !t.After(now) {
				t = t.AddDate(0, 0, 1)
			}
		case strings.HasPrefix(layout, "01-02") || strings.HasPrefix(layout, "1/2"):
			t = time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, now.Location())
			if !t.After(now) {
				t = t.AddDate(1, 0, 0)
			}
		case layout == "2006-01-02":
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
			if !t.After(now) {
				return nil, fmt.Errorf("%s is already past", s)
			}
		}
		if !t.After(now) && layout != "2006-01-02" {
			return nil, fmt.Errorf("%s is already past", s)
		}
		return &t, nil
	}
	return nil, fmt.Errorf("cannot read %q; try 20m, 09:30, or 2026-09-17 02:00", s)
}

// parseDurationLoose also accepts h/m/s words, so "2 hours" works next to "2h".
func parseDurationLoose(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	fields := strings.Fields(s)
	if len(fields) == 2 {
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			return 0, err
		}
		unit := strings.ToLower(strings.TrimSuffix(fields[1], "s"))
		switch unit {
		case "sec", "second":
			return time.Duration(n) * time.Second, nil
		case "min", "minute":
			return time.Duration(n) * time.Minute, nil
		case "hour", "hr":
			return time.Duration(n) * time.Hour, nil
		case "day":
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	return 0, fmt.Errorf("not a duration")
}
