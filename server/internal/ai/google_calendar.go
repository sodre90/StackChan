package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	googleTokenURL          = "https://oauth2.googleapis.com/token"
	googleCalendarEventsURL = "https://www.googleapis.com/calendar/v3/calendars/%s/events"
	calendarPollInterval    = 60 * time.Second
	calendarLookahead       = 24 * time.Hour
)

// googleCalendar holds OAuth2 state and announcer bookkeeping.
type googleCalendar struct {
	clientID       string
	clientSecret   string
	refreshToken   string
	calendarID     string
	reminderLeads  []time.Duration // milestones before start, sorted descending; empty disables announce

	tokenMu     sync.Mutex
	accessToken string
	tokenExpiry time.Time

	announcedMu sync.Mutex
	announced   map[string]time.Time // "eventID@leadMin" -> when we announced it
}

// calendarEvent is the trimmed view we hand to the LLM and the announcer.
type calendarEvent struct {
	ID          string    `json:"id"`
	Summary     string    `json:"summary"`
	Location    string    `json:"location,omitempty"`
	Description string    `json:"description,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	AllDay      bool      `json:"all_day"`
}

func (m *MCPManager) registerGoogleCalendarTools(cfg Config) {
	calID := cfg.GoogleCalendarID
	if calID == "" {
		calID = "primary"
	}
	// Build reminder milestones: prefer the list; fall back to the single lead.
	mins := cfg.GoogleCalendarReminderMinutes
	if len(mins) == 0 && cfg.GoogleCalendarAnnounceMinutes > 0 {
		mins = []int{cfg.GoogleCalendarAnnounceMinutes}
	}
	// Sort descending and convert to durations, dropping non-positive values.
	sort.Sort(sort.Reverse(sort.IntSlice(mins)))
	var leads []time.Duration
	for _, m := range mins {
		if m > 0 {
			leads = append(leads, time.Duration(m)*time.Minute)
		}
	}

	gc := &googleCalendar{
		clientID:      cfg.GoogleOAuthClientID,
		clientSecret:  cfg.GoogleOAuthClientSecret,
		refreshToken:  cfg.GoogleOAuthRefreshToken,
		calendarID:    calID,
		reminderLeads: leads,
		announced:     make(map[string]time.Time),
	}
	m.gcal = gc

	m.RegisterTool(MCPTool{
		Name:        "calendar.list_upcoming_events",
		Description: "List the user's upcoming Google Calendar events. Use this when the user asks about their schedule, agenda, what's next, or events on a specific day. Times in result are RFC3339 in the local timezone.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"max_results": map[string]interface{}{
					"type":        "integer",
					"description": "Max events to return (default 10, max 50).",
					"minimum":     1,
					"maximum":     50,
				},
				"hours_ahead": map[string]interface{}{
					"type":        "integer",
					"description": "Look ahead this many hours from now (default 24).",
					"minimum":     1,
					"maximum":     720,
				},
			},
		},
		Handler: m.handleCalendarListUpcoming,
	})

	if len(gc.reminderLeads) > 0 {
		go gc.runAnnouncer(context.Background())
	}
}

func (m *MCPManager) handleCalendarListUpcoming(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	gc := m.gcal
	if gc == nil {
		return "", fmt.Errorf("google calendar not configured")
	}

	maxResults := 10
	if v, ok := args["max_results"].(float64); ok && v > 0 {
		maxResults = int(v)
	}
	hoursAhead := 24
	if v, ok := args["hours_ahead"].(float64); ok && v > 0 {
		hoursAhead = int(v)
	}

	now := time.Now()
	events, err := gc.listEvents(ctx, now, now.Add(time.Duration(hoursAhead)*time.Hour), maxResults)
	if err != nil {
		return "", err
	}
	if len(events) == 0 {
		return "No upcoming events.", nil
	}

	out, _ := json.Marshal(events)
	return string(out), nil
}

// listEvents fetches events from the Google Calendar API in [timeMin, timeMax).
func (gc *googleCalendar) listEvents(ctx context.Context, timeMin, timeMax time.Time, maxResults int) ([]calendarEvent, error) {
	token, err := gc.getAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	q := url.Values{}
	q.Set("timeMin", timeMin.Format(time.RFC3339))
	q.Set("timeMax", timeMax.Format(time.RFC3339))
	q.Set("singleEvents", "true")
	q.Set("orderBy", "startTime")
	q.Set("maxResults", fmt.Sprintf("%d", maxResults))

	endpoint := fmt.Sprintf(googleCalendarEventsURL, url.PathEscape(gc.calendarID)) + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("calendar request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("calendar API error %d: %s", resp.StatusCode, string(body))
	}

	var raw struct {
		Items []struct {
			ID          string `json:"id"`
			Summary     string `json:"summary"`
			Location    string `json:"location"`
			Description string `json:"description"`
			Start       struct {
				DateTime string `json:"dateTime"`
				Date     string `json:"date"`
			} `json:"start"`
			End struct {
				DateTime string `json:"dateTime"`
				Date     string `json:"date"`
			} `json:"end"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse calendar response: %w", err)
	}

	events := make([]calendarEvent, 0, len(raw.Items))
	for _, it := range raw.Items {
		if it.Status == "cancelled" {
			continue
		}
		ev := calendarEvent{
			ID:          it.ID,
			Summary:     it.Summary,
			Location:    it.Location,
			Description: it.Description,
		}
		if it.Start.DateTime != "" {
			if t, err := time.Parse(time.RFC3339, it.Start.DateTime); err == nil {
				ev.Start = t.Local()
			}
		} else if it.Start.Date != "" {
			if t, err := time.ParseInLocation("2006-01-02", it.Start.Date, time.Local); err == nil {
				ev.Start = t
				ev.AllDay = true
			}
		}
		if it.End.DateTime != "" {
			if t, err := time.Parse(time.RFC3339, it.End.DateTime); err == nil {
				ev.End = t.Local()
			}
		} else if it.End.Date != "" {
			if t, err := time.ParseInLocation("2006-01-02", it.End.Date, time.Local); err == nil {
				ev.End = t
			}
		}
		if ev.Summary == "" {
			ev.Summary = "(no title)"
		}
		events = append(events, ev)
	}
	return events, nil
}

// getAccessToken returns a cached access token, refreshing if expired.
func (gc *googleCalendar) getAccessToken(ctx context.Context) (string, error) {
	gc.tokenMu.Lock()
	defer gc.tokenMu.Unlock()

	if gc.accessToken != "" && time.Now().Before(gc.tokenExpiry.Add(-30*time.Second)) {
		return gc.accessToken, nil
	}

	form := url.Values{}
	form.Set("client_id", gc.clientID)
	form.Set("client_secret", gc.clientSecret)
	form.Set("refresh_token", gc.refreshToken)
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, "POST", googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("token refresh: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("token refresh error %d: %s", resp.StatusCode, string(body))
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("parse token: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("empty access token in response: %s", string(body))
	}
	gc.accessToken = tok.AccessToken
	gc.tokenExpiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return gc.accessToken, nil
}

// runAnnouncer polls upcoming events and speaks each one once per reminder milestone.
func (gc *googleCalendar) runAnnouncer(ctx context.Context) {
	logger.Infof(ctx, "Calendar announcer started (leads=%v, calendar=%s)", gc.reminderLeads, gc.calendarID)
	ticker := time.NewTicker(calendarPollInterval)
	defer ticker.Stop()

	for {
		gc.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (gc *googleCalendar) tick(ctx context.Context) {
	now := time.Now()
	events, err := gc.listEvents(ctx, now, now.Add(calendarLookahead), 50)
	if err != nil {
		logger.Warningf(ctx, "calendar poll failed: %v", err)
		return
	}

	gc.pruneAnnounced(now)

	for _, ev := range events {
		if ev.AllDay {
			continue
		}
		untilStart := time.Until(ev.Start)
		if untilStart < 0 {
			continue
		}
		// Fire each milestone once, as the countdown crosses it: untilStart is in
		// (lead - pollInterval, lead]. Per-(event, lead) dedup guards against jitter.
		for _, lead := range gc.reminderLeads {
			if untilStart > lead || untilStart <= lead-calendarPollInterval {
				continue
			}
			key := fmt.Sprintf("%s@%d", ev.ID, int(lead.Minutes()))
			if !gc.markAnnounced(key, now) {
				continue
			}

			msg := hungarianReminder(ev, untilStart)
			count := SpeakToDevice(ctx, "", msg)
			logger.Infof(ctx, "Calendar announce (lead %dm) sent to %d device(s): %s",
				int(lead.Minutes()), count, msg)
		}
	}
}

// hungarianReminder builds the spoken reminder text in Hungarian.
func hungarianReminder(ev calendarEvent, untilStart time.Duration) string {
	mins := int(untilStart.Round(time.Minute).Minutes())
	var msg string
	switch {
	case mins <= 0:
		msg = fmt.Sprintf("Figyelem: %s most kezdődik.", ev.Summary)
	case mins == 1:
		msg = fmt.Sprintf("Figyelem: %s egy perc múlva kezdődik.", ev.Summary)
	default:
		msg = fmt.Sprintf("Figyelem: %s %d perc múlva kezdődik.", ev.Summary, mins)
	}
	if ev.Location != "" {
		msg += " Helyszín: " + ev.Location + "."
	}
	return msg
}

func (gc *googleCalendar) markAnnounced(id string, now time.Time) bool {
	gc.announcedMu.Lock()
	defer gc.announcedMu.Unlock()
	if _, ok := gc.announced[id]; ok {
		return false
	}
	gc.announced[id] = now
	return true
}

func (gc *googleCalendar) pruneAnnounced(now time.Time) {
	gc.announcedMu.Lock()
	defer gc.announcedMu.Unlock()
	for id, when := range gc.announced {
		if now.Sub(when) > 6*time.Hour {
			delete(gc.announced, id)
		}
	}
}
