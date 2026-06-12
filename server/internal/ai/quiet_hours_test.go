package ai

import (
	"testing"
	"time"
)

func TestIsQuietHoursAt(t *testing.T) {
	qh := QuietHoursConfig{
		Enabled:    true,
		Start:      "21:00",
		WeekdayEnd: "08:00",
		WeekendEnd: "09:00",
	}

	loc := time.Local
	tests := []struct {
		name  string
		time  time.Time
		quiet bool
	}{
		// Weekday evenings
		{"Mon 20:59", time.Date(2026, 6, 15, 20, 59, 0, 0, loc), false}, // Monday
		{"Mon 21:00", time.Date(2026, 6, 15, 21, 0, 0, 0, loc), true},
		{"Mon 23:30", time.Date(2026, 6, 15, 23, 30, 0, 0, loc), true},

		// Weekday mornings
		{"Tue 07:59", time.Date(2026, 6, 16, 7, 59, 0, 0, loc), true},  // Tuesday
		{"Tue 08:00", time.Date(2026, 6, 16, 8, 0, 0, 0, loc), false},
		{"Wed 12:00", time.Date(2026, 6, 17, 12, 0, 0, 0, loc), false}, // Wednesday

		// Friday evening into Saturday morning (weekend)
		{"Fri 21:00", time.Date(2026, 6, 12, 21, 0, 0, 0, loc), true},  // Friday
		{"Sat 08:30", time.Date(2026, 6, 13, 8, 30, 0, 0, loc), true},  // Saturday, still quiet
		{"Sat 08:59", time.Date(2026, 6, 13, 8, 59, 0, 0, loc), true},
		{"Sat 09:00", time.Date(2026, 6, 13, 9, 0, 0, 0, loc), false},
		{"Sat 15:00", time.Date(2026, 6, 13, 15, 0, 0, 0, loc), false},
		{"Sat 21:00", time.Date(2026, 6, 13, 21, 0, 0, 0, loc), true},

		// Sunday morning (weekend)
		{"Sun 08:59", time.Date(2026, 6, 14, 8, 59, 0, 0, loc), true},
		{"Sun 09:00", time.Date(2026, 6, 14, 9, 0, 0, 0, loc), false},

		// Sunday evening into Monday morning (weekday)
		{"Sun 22:00", time.Date(2026, 6, 14, 22, 0, 0, 0, loc), true},
		{"Mon 07:00", time.Date(2026, 6, 15, 7, 0, 0, 0, loc), true},
		{"Mon 08:00", time.Date(2026, 6, 15, 8, 0, 0, 0, loc), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isQuietHoursAt(tt.time, qh)
			if got != tt.quiet {
				t.Errorf("isQuietHoursAt(%s [%s]) = %v, want %v",
					tt.time.Format("Mon 15:04"), tt.time.Weekday(), got, tt.quiet)
			}
		})
	}
}

func TestIsQuietHoursDisabled(t *testing.T) {
	qh := QuietHoursConfig{Enabled: false, Start: "21:00", WeekdayEnd: "08:00", WeekendEnd: "09:00"}
	late := time.Date(2026, 6, 15, 23, 0, 0, 0, time.Local)
	if isQuietHoursAt(late, qh) {
		t.Error("expected false when disabled")
	}
}

func TestIsQuietHoursBadConfig(t *testing.T) {
	qh := QuietHoursConfig{Enabled: true, Start: "bad", WeekdayEnd: "08:00", WeekendEnd: "09:00"}
	if isQuietHoursAt(time.Now(), qh) {
		t.Error("expected false for bad config")
	}
}
