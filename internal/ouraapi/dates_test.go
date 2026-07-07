package ouraapi

import (
	"testing"
	"time"
)

// TestDefaultDateWindow pins the default window at [7 days ago, TOMORROW].
// Tomorrow is load-bearing, not an off-by-one: the live API's end_date is
// exclusive on several endpoints (daily_activity, sleep periods, workouts —
// probed 2026-07-07), so an end of today would silently drop today's
// documents, including last night's sleep. A regression to end=today would
// pass every mocked test and only fail against real data.
func TestDefaultDateWindow(t *testing.T) {
	now := time.Date(2026, 7, 7, 15, 4, 5, 0, time.UTC)
	start, end := DefaultDateWindow(now)
	if start != "2026-06-30" {
		t.Errorf("start = %q, want 2026-06-30 (7 days ago)", start)
	}
	if end != "2026-07-08" {
		t.Errorf("end = %q, want 2026-07-08 (tomorrow — end_date is exclusive on several endpoints)", end)
	}
}

// TestDefaultDateWindowCrossesMonthBoundary guards the date arithmetic where
// naive day math breaks.
func TestDefaultDateWindowCrossesMonthBoundary(t *testing.T) {
	now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
	start, end := DefaultDateWindow(now)
	if start != "2026-01-24" || end != "2026-02-01" {
		t.Errorf("window = [%s, %s], want [2026-01-24, 2026-02-01]", start, end)
	}
}
