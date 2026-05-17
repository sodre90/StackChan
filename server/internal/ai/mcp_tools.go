/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package ai

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Reminder represents a scheduled reminder created via the robot.create_reminder tool.
type Reminder struct {
	ID        string
	Message   string
	TriggerAt time.Time
	Active    bool
}

// Binary protocol message types sent to the ESP32 device.
const (
	ControlAvatar byte = 0x03
	ControlMotion byte = 0x04
	Dance         byte = 0x14
)

// MCPTool represents an MCP (Model Context Protocol) tool that the LLM can call
type MCPTool struct {
	Name        string
	Description string
	Parameters  map[string]interface{}
	Handler     func(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error)
}

// MCPManager manages MCP tools for AI interaction
type MCPManager struct {
	mu            sync.RWMutex
	tools         map[string]*MCPTool
	deviceMu      sync.RWMutex
	devices       map[string]*DeviceState // mac -> device state
	braveAPIKey   string
	haURL         string
	haToken       string
}

// DeviceState tracks the state of an ESP32 device
type DeviceState struct {
	Mac           string
	write         func(int, []byte) error // thread-safe write via AIClient.writeWS
	HeadYaw       float64
	HeadPitch     float64
	LEDRed        int
	LEDGreen      int
	LEDBlue       int
	IsSpeaking    bool
	IsOnline      bool
	LastSeen      time.Time
	Reminders     map[string]*Reminder
	ReminderID    int
	mu            sync.RWMutex
}

// NewMCPManager creates a new MCP tool manager
func NewMCPManager(cfg Config) *MCPManager {
	m := &MCPManager{
		tools:       make(map[string]*MCPTool),
		devices:     make(map[string]*DeviceState),
		braveAPIKey: cfg.BraveSearchAPIKey,
		haURL:       strings.TrimRight(cfg.HAUrl, "/"),
		haToken:     cfg.HAToken,
	}

	// Register default robot control tools
	m.RegisterTool(MCPTool{
		Name:        "robot.set_head_angles",
		Description: "Set the head yaw and pitch angles of the robot. Yaw: -90 to 90 (left to right), Pitch: -45 to 45 (down to up).",
		Parameters: map[string]interface{}{
			"type":  "object",
			"properties": map[string]interface{}{
				"yaw":   map[string]interface{}{"type": "number", "description": "Yaw angle in degrees (-90 to 90)", "minimum": -90, "maximum": 90},
				"pitch": map[string]interface{}{"type": "number", "description": "Pitch angle in degrees (-45 to 45)", "minimum": -45, "maximum": 45},
			},
			"required": []string{"yaw", "pitch"},
		},
		Handler: m.handleSetHeadAngles,
	})

	m.RegisterTool(MCPTool{
		Name:        "robot.get_head_angles",
		Description: "Get the current head yaw and pitch angles of the robot.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{},
		},
		Handler: m.handleGetHeadAngles,
	})

	m.RegisterTool(MCPTool{
		Name:        "robot.set_led_color",
		Description: "Set the tiny RGB indicator LED on the robot device itself. This is NOT a room light — only use for the robot's own LED. Values are 0-255 for each channel.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"red":   map[string]interface{}{"type": "integer", "description": "Red channel (0-255)", "minimum": 0, "maximum": 255},
				"green": map[string]interface{}{"type": "integer", "description": "Green channel (0-255)", "minimum": 0, "maximum": 255},
				"blue":  map[string]interface{}{"type": "integer", "description": "Blue channel (0-255)", "minimum": 0, "maximum": 255},
			},
			"required": []string{"red", "green", "blue"},
		},
		Handler: m.handleSetLEDColor,
	})

	m.RegisterTool(MCPTool{
		Name:        "robot.create_reminder",
		Description: "Create a timed reminder. The robot will announce the reminder when it triggers.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"message":       map[string]interface{}{"type": "string", "description": "The reminder message"},
				"delay_seconds": map[string]interface{}{"type": "integer", "description": "Delay in seconds before the reminder triggers"},
			},
			"required": []string{"message", "delay_seconds"},
		},
		Handler: m.handleCreateReminder,
	})

	m.RegisterTool(MCPTool{
		Name:        "robot.get_reminders",
		Description: "Get a list of all active reminders.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{},
		},
		Handler: m.handleGetReminders,
	})

	m.RegisterTool(MCPTool{
		Name:        "robot.stop_reminder",
		Description: "Stop a specific reminder by its ID.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"reminder_id": map[string]interface{}{"type": "string", "description": "The ID of the reminder to stop"},
			},
			"required": []string{"reminder_id"},
		},
		Handler: m.handleStopReminder,
	})

	m.RegisterTool(MCPTool{
		Name:        "robot.play_expression",
		Description: "Play an emotion/expression animation on the robot's face. Expressions: happy, sad, angry, surprised, sleepy, thinking, love, dancing.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"expression": map[string]interface{}{"type": "string", "description": "Expression to play", "enum": []string{"happy", "sad", "angry", "surprised", "sleepy", "thinking", "love", "dancing"}},
				"duration":   map[string]interface{}{"type": "integer", "description": "Duration in seconds (default 3)", "default": 3},
			},
			"required": []string{"expression"},
		},
		Handler: m.handlePlayExpression,
	})

	m.RegisterTool(MCPTool{
		Name:        "robot.play_dance",
		Description: "Play a dance animation sequence on the robot.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"dance": map[string]interface{}{"type": "string", "description": "Dance name", "enum": []string{"default", "wave", "spin", "jump"}},
			},
			"required": []string{"dance"},
		},
		Handler: m.handlePlayDance,
	})

	m.RegisterTool(MCPTool{
		Name:        "get_weather",
		Description: "Get current weather conditions and 3-day forecast for a location. Always call this for any weather or temperature question — never answer from memory.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"location": map[string]interface{}{"type": "string", "description": "City name or location (e.g. Budapest, London, New York)"},
			},
			"required": []string{"location"},
		},
		Handler: m.handleGetWeather,
	})

	m.RegisterTool(MCPTool{
		Name:        "get_current_datetime",
		Description: "Get the current date and time. Use this when the user asks what time or date it is.",
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: m.handleGetCurrentDatetime,
	})

	m.RegisterTool(MCPTool{
		Name:        "get_price",
		Description: "Get the current price and 24-hour change for a cryptocurrency or stock. Examples: 'bitcoin', 'ethereum', 'BTC', 'AAPL', 'TSLA'. Always call this for any price or market question.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"asset": map[string]interface{}{"type": "string", "description": "Crypto name/symbol (e.g. bitcoin, BTC, ethereum) or stock ticker (e.g. AAPL, TSLA)"},
			},
			"required": []string{"asset"},
		},
		Handler: m.handleGetPrice,
	})

	m.RegisterTool(MCPTool{
		Name:        "web_search",
		Description: "Search the internet for current news, recent events, or any information that might be outdated in training data. Use this whenever the user asks about something that may have changed recently.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "The search query"},
			},
			"required": []string{"query"},
		},
		Handler: m.handleWebSearch,
	})

	if m.haURL != "" && m.haToken != "" {
		m.registerHomeAssistantTools()
	}

	return m
}

// RegisterTool adds a new MCP tool
func (m *MCPManager) RegisterTool(tool MCPTool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools[tool.Name] = &tool
}

// GetTools returns all registered tools as MCP tool definitions
func (m *MCPManager) GetTools() []map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tools := make([]map[string]interface{}, 0, len(m.tools))
	for _, tool := range m.tools {
		tools = append(tools, map[string]interface{}{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": tool.Parameters,
		})
	}
	return tools
}

// GetToolDefinitions returns tools in OpenAI function-calling format
func (m *MCPManager) GetToolDefinitions() []map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	defs := make([]map[string]interface{}, 0, len(m.tools))
	for _, tool := range m.tools {
		defs = append(defs, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			},
		})
	}
	return defs
}

// RegisterDevice registers a device state for MCP tool integration.
// write must be a goroutine-safe write function (e.g. client.writeWS).
func (m *MCPManager) RegisterDevice(mac string, write func(int, []byte) error) {
	m.deviceMu.Lock()
	defer m.deviceMu.Unlock()
	m.devices[mac] = &DeviceState{
		Mac:       mac,
		write:     write,
		HeadYaw:   0,
		HeadPitch: 0,
		LEDRed:    0,
		LEDGreen:  0,
		LEDBlue:   0,
		IsOnline:  true,
		LastSeen:  time.Now(),
		Reminders: make(map[string]*Reminder),
	}
}

// GetDeviceState returns the device state for a given MAC
func (m *MCPManager) GetDeviceState(mac string) *DeviceState {
	m.deviceMu.RLock()
	defer m.deviceMu.RUnlock()
	return m.devices[mac]
}

// UpdateDeviceLastSeen updates the last seen timestamp for a device
func (m *MCPManager) UpdateDeviceLastSeen(mac string) {
	m.deviceMu.Lock()
	defer m.deviceMu.Unlock()
	if ds, ok := m.devices[mac]; ok {
		ds.LastSeen = time.Now()
	}
}

// MarkDeviceOffline marks a device as offline
func (m *MCPManager) MarkDeviceOffline(mac string) {
	m.deviceMu.Lock()
	defer m.deviceMu.Unlock()
	if ds, ok := m.devices[mac]; ok {
		ds.IsOnline = false
	}
}

// SendToDevice sends a binary message to the ESP32 device
func (m *MCPManager) SendToDevice(mac string, msgType byte, data []byte) error {
	m.deviceMu.RLock()
	ds, ok := m.devices[mac]
	m.deviceMu.RUnlock()

	if !ok || ds.write == nil {
		return fmt.Errorf("device %s not found or offline", mac)
	}

	// Build binary message: [type][length][data]
	payload := make([]byte, 1+4+len(data))
	payload[0] = msgType
	binary.BigEndian.PutUint32(payload[1:5], uint32(len(data)))
	copy(payload[5:], data)

	if err := ds.write(websocket.BinaryMessage, payload); err != nil {
		m.MarkDeviceOffline(mac)
		return fmt.Errorf("failed to send to device: %v", err)
	}

	return nil
}

// SendTextToDevice sends a text message to the ESP32 device
func (m *MCPManager) SendTextToDevice(mac string, msg interface{}) error {
	m.deviceMu.RLock()
	ds, ok := m.devices[mac]
	m.deviceMu.RUnlock()

	if !ok || ds.write == nil {
		return fmt.Errorf("device %s not found or offline", mac)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %v", err)
	}

	if err = ds.write(websocket.TextMessage, data); err != nil {
		m.MarkDeviceOffline(mac)
		return fmt.Errorf("failed to send to device: %v", err)
	}

	return nil
}

// CallTool calls a registered tool by name with the given arguments
func (m *MCPManager) CallTool(ctx context.Context, client *AIClient, name string, args map[string]interface{}) (string, error) {
	m.mu.RLock()
	tool, ok := m.tools[name]
	m.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}

	return tool.Handler(ctx, client, args)
}

// handleSetHeadAngles handles the robot.set_head_angles tool
func (m *MCPManager) handleSetHeadAngles(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	yaw, ok := args["yaw"].(float64)
	if !ok {
		return "", fmt.Errorf("yaw must be a number")
	}

	pitch, ok := args["pitch"].(float64)
	if !ok {
		return "", fmt.Errorf("pitch must be a number")
	}

	if yaw < -90 || yaw > 90 {
		return "", fmt.Errorf("yaw must be between -90 and 90")
	}
	if pitch < -45 || pitch > 45 {
		return "", fmt.Errorf("pitch must be between -45 and 45")
	}

	// Update device state
	m.deviceMu.Lock()
	if ds, exists := m.devices[client.Mac]; exists {
		ds.HeadYaw = yaw
		ds.HeadPitch = pitch
		ds.LastSeen = time.Now()
	}
	m.deviceMu.Unlock()

	// Send control motion message to device
	// Protocol: [type=0x04][length=8][yaw_int16][pitch_int16]
	data := make([]byte, 8)
	binary.BigEndian.PutUint16(data[0:2], uint16(int16(yaw*10)))   // yaw * 10 for 1 decimal precision
	binary.BigEndian.PutUint16(data[2:4], uint16(int16(pitch*10))) // pitch * 10
	err := m.SendToDevice(client.Mac, ControlMotion, data)
	if err != nil {
		return fmt.Sprintf("Head angles set to yaw=%.1f, pitch=%.1f (device not reachable)", yaw, pitch), nil
	}

	return fmt.Sprintf("Head moved to yaw=%.1f, pitch=%.1f", yaw, pitch), nil
}

// handleGetHeadAngles handles the robot.get_head_angles tool
func (m *MCPManager) handleGetHeadAngles(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	m.deviceMu.RLock()
	ds, exists := m.devices[client.Mac]
	var yaw, pitch float64
	if exists {
		yaw = ds.HeadYaw
		pitch = ds.HeadPitch
	}
	m.deviceMu.RUnlock()

	result := fmt.Sprintf(`{"yaw": %.1f, "pitch": %.1f}`, yaw, pitch)
	return result, nil
}

// handleSetLEDColor handles the robot.set_led_color tool
func (m *MCPManager) handleSetLEDColor(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	red, ok := args["red"].(float64)
	if !ok {
		return "", fmt.Errorf("red must be a number")
	}
	green, ok := args["green"].(float64)
	if !ok {
		return "", fmt.Errorf("green must be a number")
	}
	blue, ok := args["blue"].(float64)
	if !ok {
		return "", fmt.Errorf("blue must be a number")
	}

	if red < 0 || red > 255 || green < 0 || green > 255 || blue < 0 || blue > 255 {
		return "", fmt.Errorf("RGB values must be between 0 and 255")
	}

	// Update device state
	m.deviceMu.Lock()
	if ds, exists := m.devices[client.Mac]; exists {
		ds.LEDRed = int(red)
		ds.LEDGreen = int(green)
		ds.LEDBlue = int(blue)
		ds.LastSeen = time.Now()
	}
	m.deviceMu.Unlock()

	// Send control avatar message to set LED color
	// Protocol: [type=0x03][length=3][red][green][blue]
	data := []byte{byte(red), byte(green), byte(blue)}
	err := m.SendToDevice(client.Mac, ControlAvatar, data)
	if err != nil {
		return fmt.Sprintf("LED set to RGB(%d, %d, %d) (device not reachable)", int(red), int(green), int(blue)), nil
	}

	return fmt.Sprintf("LED set to RGB(%d, %d, %d)", int(red), int(green), int(blue)), nil
}

// handleCreateReminder handles the robot.create_reminder tool
func (m *MCPManager) handleCreateReminder(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	message, ok := args["message"].(string)
	if !ok {
		return "", fmt.Errorf("message must be a string")
	}

	delaySeconds, ok := args["delay_seconds"].(float64)
	if !ok {
		return "", fmt.Errorf("delay_seconds must be a number")
	}

	m.deviceMu.Lock()
	ds, exists := m.devices[client.Mac]
	if !exists {
		m.deviceMu.Unlock()
		return "", fmt.Errorf("device %s not registered", client.Mac)
	}

	ds.ReminderID++
	id := fmt.Sprintf("reminder_%d", ds.ReminderID)
	reminder := &Reminder{
		ID:        id,
		Message:   message,
		TriggerAt: time.Now().Add(time.Duration(delaySeconds) * time.Second),
		Active:    true,
	}
	ds.Reminders[id] = reminder
	m.deviceMu.Unlock()

	// Start a goroutine to trigger the reminder
	go func() {
		select {
		case <-time.After(time.Duration(delaySeconds) * time.Second):
			m.deviceMu.Lock()
			if r, rexists := ds.Reminders[id]; rexists && r.Active {
				r.Active = false
				// Send reminder announcement to device
				reminderMsg := map[string]interface{}{
					"type":  "tts",
					"state": "start",
				}
				_ = m.SendTextToDevice(client.Mac, reminderMsg)

				sentenceMsg := map[string]interface{}{
					"type":  "tts",
					"state": "sentence_start",
					"text":  fmt.Sprintf("Reminder: %s", message),
				}
				_ = m.SendTextToDevice(client.Mac, sentenceMsg)

				stopMsg := map[string]interface{}{
					"type":  "tts",
					"state": "stop",
				}
				_ = m.SendTextToDevice(client.Mac, stopMsg)

				logger.Infof(ctx, "Reminder triggered: %s", message)
			}
			m.deviceMu.Unlock()
		case <-ctx.Done():
			return
		}
	}()

	return fmt.Sprintf("Reminder created: %s (ID: %s, triggers in %.0f seconds)", message, id, delaySeconds), nil
}

// handleGetReminders handles the robot.get_reminders tool
func (m *MCPManager) handleGetReminders(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	m.deviceMu.RLock()
	ds, exists := m.devices[client.Mac]
	m.deviceMu.RUnlock()

	if !exists {
		return "No active reminders", nil
	}

	activeReminders := make([]map[string]interface{}, 0)
	ds.mu.RLock()
	for _, r := range ds.Reminders {
		if r.Active {
			activeReminders = append(activeReminders, map[string]interface{}{
				"id":         r.ID,
				"message":    r.Message,
				"trigger_at": r.TriggerAt.Format(time.RFC3339),
			})
		}
	}
	ds.mu.RUnlock()

	if len(activeReminders) == 0 {
		return "No active reminders", nil
	}

	data, _ := json.Marshal(activeReminders)
	return string(data), nil
}

// handleStopReminder handles the robot.stop_reminder tool
func (m *MCPManager) handleStopReminder(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	reminderID, ok := args["reminder_id"].(string)
	if !ok {
		return "", fmt.Errorf("reminder_id must be a string")
	}

	m.deviceMu.Lock()
	defer m.deviceMu.Unlock()

	ds, exists := m.devices[client.Mac]
	if !exists {
		return "", fmt.Errorf("device %s not registered", client.Mac)
	}

	r, rexists := ds.Reminders[reminderID]
	if !rexists {
		return "", fmt.Errorf("reminder not found: %s", reminderID)
	}

	r.Active = false
	return fmt.Sprintf("Reminder %s stopped", reminderID), nil
}

// handlePlayExpression handles the robot.play_expression tool
func (m *MCPManager) handlePlayExpression(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	expression, ok := args["expression"].(string)
	if !ok {
		return "", fmt.Errorf("expression must be a string")
	}

	validExpressions := map[string]bool{
		"happy": true, "sad": true, "angry": true, "surprised": true,
		"sleepy": true, "thinking": true, "love": true, "dancing": true,
	}
	if !validExpressions[expression] {
		return "", fmt.Errorf("invalid expression: %s. Valid: happy, sad, angry, surprised, sleepy, thinking, love, dancing", expression)
	}

	duration, ok := args["duration"].(float64)
	if !ok {
		duration = 3
	}

	// Update device state
	m.deviceMu.Lock()
	if ds, exists := m.devices[client.Mac]; exists {
		ds.LastSeen = time.Now()
	}
	m.deviceMu.Unlock()

	// Send expression command to device
	// Protocol: [type=0x03][length=1][expression_byte]
	exprBytes := map[string]byte{
		"happy": 1, "sad": 2, "angry": 3, "surprised": 4,
		"sleepy": 5, "thinking": 6, "love": 7, "dancing": 8,
	}
	data := []byte{exprBytes[expression]}
	err := m.SendToDevice(client.Mac, ControlAvatar, data)
	if err != nil {
		return fmt.Sprintf("Expression '%s' played for %.0f seconds (device not reachable)", expression, duration), nil
	}

	return fmt.Sprintf("Expression '%s' playing for %.0f seconds", expression, duration), nil
}

// handlePlayDance handles the robot.play_dance tool
func (m *MCPManager) handlePlayDance(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	dance, ok := args["dance"].(string)
	if !ok {
		return "", fmt.Errorf("dance must be a string")
	}

	validDances := map[string]bool{
		"default": true, "wave": true, "spin": true, "jump": true,
	}
	if !validDances[dance] {
		return "", fmt.Errorf("invalid dance: %s. Valid: default, wave, spin, jump", dance)
	}

	// Update device state
	m.deviceMu.Lock()
	if ds, exists := m.devices[client.Mac]; exists {
		ds.LastSeen = time.Now()
	}
	m.deviceMu.Unlock()

	// Send dance command to device
	// Protocol: [type=0x14][length=1][dance_byte]
	danceBytes := map[string]byte{
		"default": 1, "wave": 2, "spin": 3, "jump": 4,
	}
	data := []byte{danceBytes[dance]}
	err := m.SendToDevice(client.Mac, Dance, data)
	if err != nil {
		return fmt.Sprintf("Dance '%s' playing (device not reachable)", dance), nil
	}

	return fmt.Sprintf("Dance '%s' playing", dance), nil
}

// handleGetWeather fetches current weather + 3-day forecast from wttr.in
func (m *MCPManager) handleGetWeather(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	location, _ := args["location"].(string)
	if location == "" {
		location = "Budapest"
	}

	loc := strings.ReplaceAll(location, " ", "+")
	urlStr := fmt.Sprintf("https://wttr.in/%s?format=j1", loc)

	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "curl/7.79.1")

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("weather fetch failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("parse weather JSON: %w", err)
	}

	var sb strings.Builder

	// Current conditions
	if cc, ok := data["current_condition"].([]interface{}); ok && len(cc) > 0 {
		cur := cc[0].(map[string]interface{})
		desc := ""
		if descs, ok := cur["weatherDesc"].([]interface{}); ok && len(descs) > 0 {
			desc, _ = descs[0].(map[string]interface{})["value"].(string)
		}
		temp, _ := cur["temp_C"].(string)
		feels, _ := cur["FeelsLikeC"].(string)
		humidity, _ := cur["humidity"].(string)
		wind, _ := cur["windspeedKmph"].(string)
		fmt.Fprintf(&sb, "Now: %s, %s°C (feels %s°C), humidity %s%%, wind %s km/h\n",
			desc, temp, feels, humidity, wind)
	}

	// 3-day forecast
	if weather, ok := data["weather"].([]interface{}); ok {
		for _, day := range weather {
			d := day.(map[string]interface{})
			date, _ := d["date"].(string)
			maxC, _ := d["maxtempC"].(string)
			minC, _ := d["mintempC"].(string)
			desc := ""
			if hourly, ok := d["hourly"].([]interface{}); ok && len(hourly) > 4 {
				h := hourly[4].(map[string]interface{})
				if descs, ok := h["weatherDesc"].([]interface{}); ok && len(descs) > 0 {
					desc, _ = descs[0].(map[string]interface{})["value"].(string)
				}
			}
			fmt.Fprintf(&sb, "%s: %s, min %s°C / max %s°C\n", date, desc, minC, maxC)
		}
	}

	return strings.TrimSpace(sb.String()), nil
}

// handleGetCurrentDatetime returns the current date and time
func (m *MCPManager) handleGetCurrentDatetime(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	now := time.Now()
	return now.Format("2006-01-02 15:04:05 MST (Monday)"), nil
}

// binanceSymbols maps common crypto names/symbols to Binance USDT trading pairs
var binanceSymbols = map[string]string{
	"bitcoin": "BTCUSDT", "btc": "BTCUSDT",
	"ethereum": "ETHUSDT", "eth": "ETHUSDT",
	"solana": "SOLUSDT", "sol": "SOLUSDT",
	"cardano": "ADAUSDT", "ada": "ADAUSDT",
	"dogecoin": "DOGEUSDT", "doge": "DOGEUSDT",
	"ripple": "XRPUSDT", "xrp": "XRPUSDT",
	"polkadot": "DOTUSDT", "dot": "DOTUSDT",
	"chainlink": "LINKUSDT", "link": "LINKUSDT",
	"litecoin": "LTCUSDT", "ltc": "LTCUSDT",
	"avalanche": "AVAXUSDT", "avax": "AVAXUSDT",
	"shiba": "SHIBUSDT", "shib": "SHIBUSDT",
	"matic": "MATICUSDT", "polygon": "MATICUSDT",
	"uniswap": "UNIUSDT", "uni": "UNIUSDT",
	"bnb": "BNBUSDT", "binancecoin": "BNBUSDT",
}

// handleGetPrice fetches crypto price from Binance or stock price from Yahoo Finance
func (m *MCPManager) handleGetPrice(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	asset, _ := args["asset"].(string)
	if asset == "" {
		return "", fmt.Errorf("asset must be a non-empty string")
	}

	lower := strings.ToLower(strings.TrimSpace(asset))
	if sym, ok := binanceSymbols[lower]; ok {
		return fetchCryptoPrice(ctx, sym, asset)
	}
	return fetchStockPrice(ctx, strings.ToUpper(strings.TrimSpace(asset)))
}

// fetchCryptoPrice queries Binance for a crypto price (free, no API key, generous limits)
func fetchCryptoPrice(ctx context.Context, symbol, displayName string) (string, error) {
	u := "https://api.binance.com/api/v3/ticker/24hr?symbol=" + url.QueryEscape(symbol)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "StackChan/1.0")

	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("Binance request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 400 {
		return "", fmt.Errorf("unknown crypto symbol: %s", displayName)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("parse Binance response: %w", err)
	}

	priceStr, _ := data["lastPrice"].(string)
	changeStr, _ := data["priceChangePercent"].(string)

	price := 0.0
	fmt.Sscanf(priceStr, "%f", &price)
	change := 0.0
	fmt.Sscanf(changeStr, "%f", &change)

	sign := "+"
	if change < 0 {
		sign = ""
	}
	return fmt.Sprintf("%s: $%.2f (%s%.2f%% 24h)", displayName, price, sign, change), nil
}

// fetchStockPrice queries Yahoo Finance for a stock price
func fetchStockPrice(ctx context.Context, ticker string) (string, error) {
	u := fmt.Sprintf("https://query1.finance.yahoo.com/v8/finance/chart/%s?interval=1d&range=1d",
		url.QueryEscape(ticker))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; StackChan/1.0)")

	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("Yahoo Finance request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return "", fmt.Errorf("ticker %s not found", ticker)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("parse Yahoo Finance response: %w", err)
	}

	chart, _ := data["chart"].(map[string]interface{})
	results, _ := chart["result"].([]interface{})
	if len(results) == 0 {
		return "", fmt.Errorf("no data for ticker %s", ticker)
	}

	result := results[0].(map[string]interface{})
	meta, _ := result["meta"].(map[string]interface{})

	price, _ := meta["regularMarketPrice"].(float64)
	prevClose, _ := meta["chartPreviousClose"].(float64)
	currency, _ := meta["currency"].(string)
	name, _ := meta["shortName"].(string)
	if name == "" {
		name = ticker
	}

	var changeStr string
	if prevClose > 0 {
		change := ((price - prevClose) / prevClose) * 100
		sign := "+"
		if change < 0 {
			sign = ""
		}
		changeStr = fmt.Sprintf(" (%s%.2f%% today)", sign, change)
	}

	return fmt.Sprintf("%s (%s): %.2f %s%s", name, ticker, price, currency, changeStr), nil
}

// handleWebSearch searches for current information using DDG instant answers or Brave Search
func (m *MCPManager) handleWebSearch(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query must be a non-empty string")
	}

	// Brave Search API (if configured) — full web results
	if m.braveAPIKey != "" {
		result, err := braveSearch(ctx, m.braveAPIKey, query)
		if err == nil && result != "" {
			return result, nil
		}
	}

	// DuckDuckGo Instant Answer — free, no key, works for factual queries
	if result := ddgInstantAnswer(ctx, query); result != "" {
		return result, nil
	}

	return fmt.Sprintf("No instant answer found for: %q. For full web search results, configure brave_search_api_key in config.yaml (free at search.brave.com).", query), nil
}

// braveSearch queries the Brave Search API for web results
func braveSearch(ctx context.Context, apiKey, query string) (string, error) {
	u := "https://api.search.brave.com/res/v1/web/search?q=" + url.QueryEscape(query) + "&count=5"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Subscription-Token", apiKey)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Brave Search returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}

	web, _ := data["web"].(map[string]interface{})
	results, _ := web["results"].([]interface{})
	if len(results) == 0 {
		return "", fmt.Errorf("no results")
	}

	var lines []string
	for i, r := range results {
		item, _ := r.(map[string]interface{})
		title, _ := item["title"].(string)
		desc, _ := item["description"].(string)
		if title != "" && desc != "" {
			lines = append(lines, fmt.Sprintf("%s: %s", title, desc))
		} else if title != "" {
			lines = append(lines, title)
		}
		if i >= 4 {
			break
		}
	}
	return strings.Join(lines, "\n\n"), nil
}

// ddgInstantAnswer queries the DuckDuckGo Instant Answer API (free, no key required)
func ddgInstantAnswer(ctx context.Context, query string) string {
	u := "https://api.duckduckgo.com/?q=" + url.QueryEscape(query) + "&format=json&no_html=1&skip_disambig=1"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "StackChan/1.0")

	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}

	abstract, _ := data["AbstractText"].(string)
	if abstract != "" {
		source, _ := data["AbstractSource"].(string)
		if source != "" {
			return fmt.Sprintf("%s (Source: %s)", abstract, source)
		}
		return abstract
	}

	answer, _ := data["Answer"].(string)
	return answer
}

// stripHTMLTags removes HTML tags from a string
func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

