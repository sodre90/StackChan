package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const geminiDefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

func geminiGenerateURL() string {
	base := geminiDefaultBaseURL
	return fmt.Sprintf("%s/models/%s:generateContent?key=%s",
		base, aiConfig.LLMModel, aiConfig.APIKey)
}

func geminiStreamURL() string {
	base := geminiDefaultBaseURL
	return fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse&key=%s",
		base, aiConfig.LLMModel, aiConfig.APIKey)
}

func buildGeminiRequest(systemPrompt string, contextMessages []map[string]interface{}) map[string]interface{} {
	var sysInst map[string]interface{}
	if systemPrompt != "" {
		sysInst = map[string]interface{}{
			"parts": []map[string]interface{}{{"text": systemPrompt}},
		}
	}

	var contents []map[string]interface{}
	for _, msg := range contextMessages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)

		var geminiRole string
		switch role {
		case "user":
			geminiRole = "user"
		case "assistant":
			geminiRole = "model"
		default:
			continue
		}

		contents = append(contents, map[string]interface{}{
			"role":  geminiRole,
			"parts": []map[string]interface{}{{"text": content}},
		})
	}

	req := map[string]interface{}{
		"contents": contents,
		"generationConfig": map[string]interface{}{
			"temperature":     0.7,
			"maxOutputTokens": 512,
		},
	}
	if sysInst != nil {
		req["systemInstruction"] = sysInst
	}
	return req
}

func geminiToolDeclarations() []map[string]interface{} {
	if mcpManager == nil {
		return nil
	}
	openaiTools := mcpManager.GetToolDefinitions()
	var decls []map[string]interface{}
	for _, t := range openaiTools {
		fn, ok := t["function"].(map[string]interface{})
		if !ok {
			continue
		}
		decls = append(decls, map[string]interface{}{
			"name":        fn["name"],
			"description": fn["description"],
			"parameters":  fn["parameters"],
		})
	}
	if len(decls) == 0 {
		return nil
	}
	return []map[string]interface{}{
		{"functionDeclarations": decls},
	}
}

func extractGeminiText(result map[string]interface{}) string {
	candidates, ok := result["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return ""
	}
	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return ""
	}
	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return ""
	}
	parts, ok := content["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		return ""
	}

	var out []string
	for _, p := range parts {
		part, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if thought, ok := part["thought"].(bool); ok && thought {
			continue
		}
		if text, ok := part["text"].(string); ok {
			out = append(out, text)
		}
	}
	return strings.Join(out, "")
}

func extractGeminiFunctionCalls(result map[string]interface{}) []map[string]interface{} {
	candidates, ok := result["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return nil
	}
	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return nil
	}
	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return nil
	}
	parts, ok := content["parts"].([]interface{})
	if !ok {
		return nil
	}
	var calls []map[string]interface{}
	for _, p := range parts {
		part, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if fc, ok := part["functionCall"].(map[string]interface{}); ok {
			calls = append(calls, fc)
		}
	}
	return calls
}

func extractGeminiModelContent(result map[string]interface{}) map[string]interface{} {
	candidates, ok := result["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return nil
	}
	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return nil
	}
	content, _ := candidate["content"].(map[string]interface{})
	return content
}

func callLLMGemini(ctx context.Context, client *AIClient) string {
	if aiConfig.APIKey == "" {
		logger.Warning(ctx, "Gemini API key not configured")
		return ""
	}

	if aiConfig.EnableMCPTools && mcpManager != nil {
		return callLLMGeminiWithTools(ctx, client)
	}

	systemPrompt := aiConfig.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "You are a helpful AI assistant."
	}

	contextMessages := getContextMessages(ctx, client)
	requestBody := buildGeminiRequest(systemPrompt, contextMessages)

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		logger.Errorf(ctx, "Failed to marshal Gemini request: %v", err)
		return ""
	}

	req, err := http.NewRequestWithContext(ctx, "POST", geminiGenerateURL(), bytes.NewReader(bodyBytes))
	if err != nil {
		logger.Errorf(ctx, "Failed to create Gemini request: %v", err)
		return ""
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 60 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		logger.Errorf(ctx, "Gemini request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()

	responseBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf(ctx, "Failed to read Gemini response: %v", err)
		return ""
	}

	if resp.StatusCode != http.StatusOK {
		logger.Errorf(ctx, "Gemini API error (status %d): %s", resp.StatusCode, string(responseBytes))
		return ""
	}

	var result map[string]interface{}
	if err := json.Unmarshal(responseBytes, &result); err != nil {
		logger.Errorf(ctx, "Failed to parse Gemini response: %v", err)
		return ""
	}

	response := strings.TrimSpace(extractGeminiText(result))
	if response != "" {
		logger.Infof(ctx, "Gemini response: %s", response)
	}
	return response
}

func callLLMGeminiWithTools(ctx context.Context, client *AIClient) string {
	if aiConfig.APIKey == "" {
		return ""
	}

	systemPrompt := aiConfig.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "You are a helpful AI assistant."
	}

	contextMessages := getContextMessages(ctx, client)
	requestBody := buildGeminiRequest(systemPrompt, contextMessages)

	tools := geminiToolDeclarations()
	httpClient := &http.Client{Timeout: 60 * time.Second}

	contents, _ := requestBody["contents"].([]map[string]interface{})

	for iteration := 0; iteration < 5; iteration++ {
		requestBody["contents"] = contents
		if tools != nil {
			requestBody["tools"] = tools
		}

		bodyBytes, err := json.Marshal(requestBody)
		if err != nil {
			logger.Errorf(ctx, "Failed to marshal Gemini request: %v", err)
			return ""
		}

		req, err := http.NewRequestWithContext(ctx, "POST", geminiGenerateURL(), bytes.NewReader(bodyBytes))
		if err != nil {
			logger.Errorf(ctx, "Failed to create Gemini request: %v", err)
			return ""
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			logger.Errorf(ctx, "Gemini request failed: %v", err)
			return ""
		}
		responseBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			logger.Errorf(ctx, "Failed to read Gemini response: %v", err)
			return ""
		}
		if resp.StatusCode != http.StatusOK {
			logger.Errorf(ctx, "Gemini API error (status %d): %s", resp.StatusCode, string(responseBytes))
			return ""
		}

		var result map[string]interface{}
		if err := json.Unmarshal(responseBytes, &result); err != nil {
			logger.Errorf(ctx, "Failed to parse Gemini response: %v", err)
			return ""
		}

		funcCalls := extractGeminiFunctionCalls(result)
		if len(funcCalls) > 0 {
			if modelContent := extractGeminiModelContent(result); modelContent != nil {
				contents = append(contents, modelContent)
			}

			var responseParts []map[string]interface{}
			for _, fc := range funcCalls {
				toolName, _ := fc["name"].(string)
				toolArgs, _ := fc["args"].(map[string]interface{})

				logger.Infof(ctx, "Gemini tool call: %s args=%v", toolName, toolArgs)
				toolResult, err := mcpManager.CallTool(ctx, client, toolName, toolArgs)
				if err != nil {
					toolResult = fmt.Sprintf("Error: %v", err)
				}
				logger.Infof(ctx, "Tool %s result: %s", toolName, toolResult)

				responseParts = append(responseParts, map[string]interface{}{
					"functionResponse": map[string]interface{}{
						"name": toolName,
						"response": map[string]interface{}{
							"result": toolResult,
						},
					},
				})
			}

			contents = append(contents, map[string]interface{}{
				"role":  "function",
				"parts": responseParts,
			})
			continue
		}

		response := strings.TrimSpace(extractGeminiText(result))
		if response != "" {
			logger.Infof(ctx, "Gemini (tools) response: %s", response)
		}
		return response
	}
	return ""
}

func streamLLMSentencesGemini(ctx context.Context, client *AIClient) string {
	if aiConfig.APIKey == "" {
		logger.Warning(ctx, "Gemini API key not configured")
		return ""
	}

	systemPrompt := aiConfig.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "You are a helpful AI assistant."
	}

	contextMessages := getContextMessages(ctx, client)
	requestBody := buildGeminiRequest(systemPrompt, contextMessages)

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		logger.Errorf(ctx, "Failed to marshal Gemini request: %v", err)
		return ""
	}

	req, err := http.NewRequestWithContext(ctx, "POST", geminiStreamURL(), bytes.NewReader(bodyBytes))
	if err != nil {
		logger.Errorf(ctx, "Failed to create Gemini request: %v", err)
		return ""
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 60 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		logger.Errorf(ctx, "Gemini streaming request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logger.Errorf(ctx, "Gemini streaming API error (status %d): %s", resp.StatusCode, string(body))
		return ""
	}

	var acc sentenceAccumulator
	var assembled strings.Builder

	speak := func(sentence string) {
		if ctx.Err() != nil {
			return
		}
		sentence = stripEmojis(sentence)
		if sentence == "" {
			return
		}
		assembled.WriteString(sentence)
		assembled.WriteByte(' ')
		sendTTS(ctx, client, "sentence_start", sentence)
		if aiConfig.EnableTTS {
			if audio := generateSpeech(ctx, sentence); len(audio) > 0 {
				sendAudioChunks(ctx, client, audio)
			}
		}
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		text := extractGeminiText(chunk)
		if text == "" {
			continue
		}

		for _, sentence := range acc.feed(text) {
			speak(sentence)
		}
	}

	if err := scanner.Err(); err != nil {
		logger.Errorf(ctx, "Gemini streaming error: %v", err)
	}

	if remainder := acc.drain(); remainder != "" {
		speak(remainder)
	}

	response := strings.TrimSpace(assembled.String())
	if response != "" {
		logger.Infof(ctx, "Gemini sentence-stream response: %s", response)
	}
	return response
}
