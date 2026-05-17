package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (m *MCPManager) registerHomeAssistantTools() {
	m.RegisterTool(MCPTool{
		Name:        "home.get_entities",
		Description: "List available Home Assistant entities. Use this first to discover entity IDs before controlling devices. Filter by domain: 'light', 'switch', 'script', 'sensor', 'climate', 'cover', 'lock', etc. IMPORTANT: Many devices are controlled via scripts. Always search domain 'script' as well if the first domain search does not have an obvious match.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"domain": map[string]interface{}{
					"type":        "string",
					"description": "Entity domain to filter by (e.g. 'light', 'switch', 'script', 'sensor'). Leave empty to list all.",
				},
			},
		},
		Handler: m.handleHAGetEntities,
	})

	m.RegisterTool(MCPTool{
		Name:        "home.turn_on",
		Description: "Turn on / open / unlock a Home Assistant entity (light, switch, cover, lock, etc.). For lights, you can optionally set brightness (0-255) and color.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"entity_id":  map[string]interface{}{"type": "string", "description": "Entity ID (e.g. 'light.living_room', 'switch.fan')"},
				"brightness": map[string]interface{}{"type": "integer", "description": "Brightness 0-255 (lights only, optional)", "minimum": 0, "maximum": 255},
				"color":      map[string]interface{}{"type": "string", "description": "Color name like 'red', 'blue', 'warm_white' (lights only, optional)"},
			},
			"required": []string{"entity_id"},
		},
		Handler: m.handleHATurnOn,
	})

	m.RegisterTool(MCPTool{
		Name:        "home.turn_off",
		Description: "Turn off / close / lock a Home Assistant entity (light, switch, cover, lock, etc.).",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"entity_id": map[string]interface{}{"type": "string", "description": "Entity ID (e.g. 'light.living_room', 'switch.fan')"},
			},
			"required": []string{"entity_id"},
		},
		Handler: m.handleHATurnOff,
	})

	m.RegisterTool(MCPTool{
		Name:        "home.call_script",
		Description: "Run a Home Assistant script. Use home.get_entities with domain 'script' to discover available scripts.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"entity_id": map[string]interface{}{"type": "string", "description": "Script entity ID (e.g. 'script.good_morning', 'script.movie_mode')"},
			},
			"required": []string{"entity_id"},
		},
		Handler: m.handleHACallScript,
	})

	m.RegisterTool(MCPTool{
		Name:        "home.get_state",
		Description: "Get the current state of a Home Assistant entity (on/off, temperature, sensor reading, etc.).",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"entity_id": map[string]interface{}{"type": "string", "description": "Entity ID (e.g. 'light.living_room', 'sensor.temperature')"},
			},
			"required": []string{"entity_id"},
		},
		Handler: m.handleHAGetState,
	})
}

func domainOnService(domain string) string {
	switch domain {
	case "cover":
		return "open_cover"
	case "lock":
		return "unlock"
	default:
		return "turn_on"
	}
}

func domainOffService(domain string) string {
	switch domain {
	case "cover":
		return "close_cover"
	case "lock":
		return "lock"
	default:
		return "turn_off"
	}
}

func (m *MCPManager) haRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, m.haURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.haToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("HA request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HA API error %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

func (m *MCPManager) handleHAGetEntities(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	domain, _ := args["domain"].(string)

	data, err := m.haRequest(ctx, "GET", "/api/states", nil)
	if err != nil {
		return "", fmt.Errorf("fetch entities: %w", err)
	}

	var states []map[string]interface{}
	if err := json.Unmarshal(data, &states); err != nil {
		return "", fmt.Errorf("parse entities: %w", err)
	}

	type entityInfo struct {
		ID    string `json:"entity_id"`
		State string `json:"state"`
		Name  string `json:"name,omitempty"`
	}

	var entities []entityInfo
	for _, s := range states {
		eid, _ := s["entity_id"].(string)
		if eid == "" {
			continue
		}
		includeScripts := domain == "" || domain == "cover" || domain == "lock" || domain == "switch"
		if domain != "" {
			if !strings.HasPrefix(eid, domain+".") && !(includeScripts && strings.HasPrefix(eid, "script.")) {
				continue
			}
		}

		state, _ := s["state"].(string)
		name := ""
		if attrs, ok := s["attributes"].(map[string]interface{}); ok {
			name, _ = attrs["friendly_name"].(string)
		}

		entities = append(entities, entityInfo{ID: eid, State: state, Name: name})
	}

	if len(entities) == 0 {
		if domain != "" {
			return fmt.Sprintf("No entities found with domain '%s'", domain), nil
		}
		return "No entities found", nil
	}

	result, _ := json.Marshal(entities)
	return string(result), nil
}

func (m *MCPManager) haEntityExists(ctx context.Context, entityID string) (bool, error) {
	_, err := m.haRequest(ctx, "GET", "/api/states/"+entityID, nil)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (m *MCPManager) handleHATurnOn(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	entityID, ok := args["entity_id"].(string)
	if !ok || entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}

	exists, err := m.haEntityExists(ctx, entityID)
	if err != nil {
		return "", fmt.Errorf("check entity: %w", err)
	}
	if !exists {
		return fmt.Sprintf("Entity %s not found. Call home.get_entities to find the correct entity_id.", entityID), nil
	}

	serviceData := map[string]interface{}{
		"entity_id": entityID,
	}

	if brightness, ok := args["brightness"].(float64); ok {
		serviceData["brightness"] = int(brightness)
	}

	if color, ok := args["color"].(string); ok && color != "" {
		serviceData["color_name"] = color
	}

	domain := "homeassistant"
	if idx := strings.Index(entityID, "."); idx > 0 {
		domain = entityID[:idx]
	}

	service := domainOnService(domain)

	_, err = m.haRequest(ctx, "POST", "/api/services/"+domain+"/"+service, serviceData)
	if err != nil {
		return "", fmt.Errorf("turn on %s: %w", entityID, err)
	}

	return fmt.Sprintf("Turned on %s", entityID), nil
}

func (m *MCPManager) handleHATurnOff(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	entityID, ok := args["entity_id"].(string)
	if !ok || entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}

	exists, err := m.haEntityExists(ctx, entityID)
	if err != nil {
		return "", fmt.Errorf("check entity: %w", err)
	}
	if !exists {
		return fmt.Sprintf("Entity %s not found. Call home.get_entities to find the correct entity_id.", entityID), nil
	}

	domain := "homeassistant"
	if idx := strings.Index(entityID, "."); idx > 0 {
		domain = entityID[:idx]
	}

	serviceData := map[string]interface{}{
		"entity_id": entityID,
	}

	service := domainOffService(domain)

	_, err = m.haRequest(ctx, "POST", "/api/services/"+domain+"/"+service, serviceData)
	if err != nil {
		return "", fmt.Errorf("turn off %s: %w", entityID, err)
	}

	return fmt.Sprintf("Turned off %s", entityID), nil
}

func (m *MCPManager) handleHACallScript(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	entityID, ok := args["entity_id"].(string)
	if !ok || entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}

	if !strings.HasPrefix(entityID, "script.") {
		entityID = "script." + entityID
	}

	exists, err := m.haEntityExists(ctx, entityID)
	if err != nil {
		return "", fmt.Errorf("check script: %w", err)
	}
	if !exists {
		return fmt.Sprintf("Script %s not found. Call home.get_entities with domain 'script' to find the correct script name.", entityID), nil
	}

	serviceData := map[string]interface{}{
		"entity_id": entityID,
	}

	_, err = m.haRequest(ctx, "POST", "/api/services/script/turn_on", serviceData)
	if err != nil {
		return "", fmt.Errorf("call script %s: %w", entityID, err)
	}

	return fmt.Sprintf("Script %s executed", entityID), nil
}

func (m *MCPManager) handleHAGetState(ctx context.Context, client *AIClient, args map[string]interface{}) (string, error) {
	entityID, ok := args["entity_id"].(string)
	if !ok || entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}

	data, err := m.haRequest(ctx, "GET", "/api/states/"+entityID, nil)
	if err != nil {
		return "", fmt.Errorf("get state of %s: %w", entityID, err)
	}

	var state map[string]interface{}
	if err := json.Unmarshal(data, &state); err != nil {
		return "", fmt.Errorf("parse state: %w", err)
	}

	stateVal, _ := state["state"].(string)
	attrs, _ := state["attributes"].(map[string]interface{})
	name, _ := attrs["friendly_name"].(string)

	info := map[string]interface{}{
		"entity_id": entityID,
		"state":     stateVal,
		"name":      name,
	}

	// Include useful attributes
	for _, key := range []string{"brightness", "color_temp", "rgb_color", "temperature", "humidity", "unit_of_measurement", "current_temperature", "hvac_action"} {
		if v, ok := attrs[key]; ok {
			info[key] = v
		}
	}

	result, _ := json.Marshal(info)
	return string(result), nil
}
