package ai

import (
	"strconv"
	"strings"
	"time"
)

// QuietHoursConfig defines a nightly do-not-disturb window.
// Start is the same every day; the morning end differs between weekdays and weekends.
type QuietHoursConfig struct {
	Enabled    bool   `yaml:"enabled" json:"enabled"`
	Start      string `yaml:"start" json:"start"`
	WeekdayEnd string `yaml:"weekday_end" json:"weekday_end"`
	WeekendEnd string `yaml:"weekend_end" json:"weekend_end"`
}

func isQuietHours() bool {
	return isQuietHoursAt(time.Now(), aiConfig.QuietHours)
}

func isQuietHoursAt(now time.Time, qh QuietHoursConfig) bool {
	if !qh.Enabled {
		return false
	}

	startMin := parseTimeOfDay(qh.Start)
	weekdayEndMin := parseTimeOfDay(qh.WeekdayEnd)
	weekendEndMin := parseTimeOfDay(qh.WeekendEnd)

	if startMin < 0 || weekdayEndMin < 0 || weekendEndMin < 0 {
		return false
	}

	nowMin := now.Hour()*60 + now.Minute()

	// Evening: at or after start → always quiet
	if nowMin >= startMin {
		return true
	}

	// Morning: before end, which depends on the current day of week
	endMin := weekdayEndMin
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		endMin = weekendEndMin
	}

	return nowMin < endMin
}

// parseTimeOfDay parses "HH:MM" to minutes since midnight. Returns -1 on error.
func parseTimeOfDay(s string) int {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return -1
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return -1
	}
	return h*60 + m
}
