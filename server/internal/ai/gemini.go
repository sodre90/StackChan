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

func systemPromptWithDate(base string) string {
	if base == "" {
		base = "You are a helpful AI assistant."
	}
	dateStr := time.Now().Format("2006-01-02 (Monday)")
	return "Today's date is " + dateStr + ".\n\n" + base
}

// newGeminiHTTPClient builds the HTTP client used for all Gemini calls. The
// overall Timeout bounds the whole request (generous for streaming short
// replies), while ResponseHeaderTimeout bounds only the wait for the response
// headers. SSE responses ("alt=sse") return their 200 headers almost
// immediately, so a healthy request always clears this in well under a second.
// A stalled or overloaded primary model (we've seen gemini-3.5-flash hang the
// full 60s "awaiting headers", or return 503 after ~17s) instead trips the
// header timeout fast, so geminiPostWithFallback can fail over to the healthy
// fallback model in seconds rather than after a full minute. This does NOT
// truncate legitimate streaming — only the time-to-first-byte phase is bounded.
func newGeminiHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 8 * time.Second
	return &http.Client{
		Timeout:   60 * time.Second,
		Transport: transport,
	}
}

func geminiGenerateURL(model string) string {
	base := geminiDefaultBaseURL
	return fmt.Sprintf("%s/models/%s:generateContent?key=%s",
		base, model, aiConfig.APIKey)
}

func geminiStreamURL(model string) string {
	base := geminiDefaultBaseURL
	return fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse&key=%s",
		base, model, aiConfig.APIKey)
}

// geminiModelChain returns the models to try in priority order: the primary
// LLMModel first, then LLMFallbackModels. Empties and duplicates are removed.
func geminiModelChain() []string {
	chain := make([]string, 0, 1+len(aiConfig.LLMFallbackModels))
	seen := make(map[string]bool)
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m != "" && !seen[m] {
			seen[m] = true
			chain = append(chain, m)
		}
	}
	add(aiConfig.LLMModel)
	for _, m := range aiConfig.LLMFallbackModels {
		add(m)
	}
	return chain
}

// geminiPostWithFallback POSTs body to the Gemini endpoint built by urlForModel,
// trying models[startIdx:] in priority order. It advances to the next model on a
// rate-limit (429) or server-unavailable (5xx) response (or a transport error),
// since each model has its own quota. Other responses (2xx, or hard 4xx like a
// bad request) are returned to the caller as-is. Returns the live response, the
// index of the model that produced it, or an error if every model was exhausted.
func geminiPostWithFallback(ctx context.Context, httpClient *http.Client,
	urlForModel func(model string) string, body []byte, startIdx int) (*http.Response, int, error) {

	models := geminiModelChain()
	if len(models) == 0 {
		return nil, 0, fmt.Errorf("no LLM models configured")
	}
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx >= len(models) {
		startIdx = len(models) - 1
	}

	lastStatus := 0
	lastBody := ""
	for idx := startIdx; idx < len(models); idx++ {
		req, err := http.NewRequestWithContext(ctx, "POST", urlForModel(models[idx]), bytes.NewReader(body))
		if err != nil {
			return nil, idx, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			lastStatus, lastBody = 0, err.Error()
			logger.Warningf(ctx, "Gemini request to %q failed: %v", models[idx], err)
			continue // transport error — try next model
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastStatus, lastBody = resp.StatusCode, string(b)
			if idx+1 < len(models) {
				logger.Warningf(ctx, "Gemini model %q unavailable (status %d), falling back to %q",
					models[idx], resp.StatusCode, models[idx+1])
			} else {
				logger.Errorf(ctx, "Gemini model %q unavailable (status %d) and no fallback left",
					models[idx], resp.StatusCode)
			}
			continue // rate-limited / overloaded — try next model
		}

		if idx > startIdx {
			logger.Infof(ctx, "Gemini using fallback model %q", models[idx])
		}
		return resp, idx, nil
	}
	return nil, len(models) - 1, fmt.Errorf("all Gemini models exhausted (last status %d): %s", lastStatus, lastBody)
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
			"thinkingConfig": map[string]interface{}{
				"thinkingBudget": 0,
			},
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

// extractGeminiFunctionCallParts returns full part objects for any functionCall parts,
// preserving fields like thought_signature that Gemini requires on follow-up requests.
func extractGeminiFunctionCallParts(result map[string]interface{}) []map[string]interface{} {
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
		if _, ok := part["functionCall"].(map[string]interface{}); ok {
			calls = append(calls, part)
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

	systemPrompt := systemPromptWithDate(aiConfig.SystemPrompt)

	contextMessages := getContextMessages(ctx, client)
	requestBody := buildGeminiRequest(systemPrompt, contextMessages)

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		logger.Errorf(ctx, "Failed to marshal Gemini request: %v", err)
		return ""
	}

	httpClient := newGeminiHTTPClient()
	resp, _, err := geminiPostWithFallback(ctx, httpClient, geminiGenerateURL, bodyBytes, 0)
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

	systemPrompt := systemPromptWithDate(aiConfig.SystemPrompt)

	contextMessages := getContextMessages(ctx, client)
	requestBody := buildGeminiRequest(systemPrompt, contextMessages)

	tools := geminiToolDeclarations()
	httpClient := newGeminiHTTPClient()

	contents, _ := requestBody["contents"].([]map[string]interface{})

	modelIdx := 0 // persists across tool iterations: once we fall back, stay there
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

		resp, usedIdx, err := geminiPostWithFallback(ctx, httpClient, geminiGenerateURL, bodyBytes, modelIdx)
		if err != nil {
			logger.Errorf(ctx, "Gemini request failed: %v", err)
			return ""
		}
		modelIdx = usedIdx
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

	systemPrompt := systemPromptWithDate(aiConfig.SystemPrompt)

	contextMessages := getContextMessages(ctx, client)
	requestBody := buildGeminiRequest(systemPrompt, contextMessages)

	// Include tool declarations when MCP is enabled so tool calls work in streaming mode.
	var toolDecls interface{}
	if aiConfig.EnableMCPTools && mcpManager != nil {
		toolDecls = geminiToolDeclarations()
	}

	contents, _ := requestBody["contents"].([]map[string]interface{})

	var assembled strings.Builder
	httpClient := newGeminiHTTPClient()

	// TTS pipeline: render sentences across a worker pool concurrently with
	// playback, preserving order, so audio for upcoming sentences is ready before
	// the current one finishes. A single serial renderer underruns the device's
	// jitter buffer on short sentences while the next cloud TTS render (~1–1.7s)
	// is still running, which is heard as a stutter between sentences.
	pipeline := newTTSPipeline(ctx, client)

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
		// Hand the cumulative answer-so-far to the pipeline; it emits the
		// sentence_start (the full text up to here) right before this sentence's
		// audio, so the bubble grows in step with the voice rather than all at once.
		pipeline.submit(sentence, strings.TrimSpace(assembled.String()))
	}

	modelIdx := 0 // persists across tool iterations: once we fall back, stay there
	for iteration := 0; iteration < 5; iteration++ {
		if ctx.Err() != nil {
			break
		}

		requestBody["contents"] = contents
		if toolDecls != nil {
			requestBody["tools"] = toolDecls
		}

		bodyBytes, err := json.Marshal(requestBody)
		if err != nil {
			logger.Errorf(ctx, "Failed to marshal Gemini request: %v", err)
			break
		}

		iterStart := time.Now()
		logger.Debugf(ctx, "Gemini stream iter=%d sending request", iteration)

		// Send with model fallback: on rate-limit (429) / unavailable (5xx),
		// geminiPostWithFallback advances to the next model in the priority chain.
		resp, usedIdx, err := geminiPostWithFallback(ctx, httpClient,
			geminiStreamURL, bodyBytes, modelIdx)
		if err != nil {
			logger.Errorf(ctx, "Gemini streaming request failed: %v", err)
			break
		}
		modelIdx = usedIdx

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			logger.Errorf(ctx, "Gemini streaming API error (status %d): %s", resp.StatusCode, string(body))
			break
		}

		var acc sentenceAccumulator
		// funcCallParts stores full part objects so thought_signature is preserved.
		var funcCallParts []map[string]interface{}
		var firstTokenAt time.Time
		var chunkCount, thoughtChunks int

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
			chunkCount++
			chunkAge := time.Since(iterStart)

			// Check for thought parts (thinking model tokens we filter out)
			isThought := false
			if cands, ok := chunk["candidates"].([]interface{}); ok && len(cands) > 0 {
				if cand, ok := cands[0].(map[string]interface{}); ok {
					if content, ok := cand["content"].(map[string]interface{}); ok {
						if parts, ok := content["parts"].([]interface{}); ok {
							for _, p := range parts {
								if part, ok := p.(map[string]interface{}); ok {
									if thought, ok := part["thought"].(bool); ok && thought {
										isThought = true
										thoughtChunks++
									}
								}
							}
						}
					}
				}
			}
			logger.Debugf(ctx, "Gemini stream iter=%d chunk=%d at=%.0fms thought=%v", iteration, chunkCount, float64(chunkAge.Milliseconds()), isThought)

			if text := extractGeminiText(chunk); text != "" {
				if firstTokenAt.IsZero() {
					firstTokenAt = time.Now()
					logger.Debugf(ctx, "Gemini stream iter=%d TTFT=%.0fms", iteration, float64(time.Since(iterStart).Milliseconds()))
				}
				for _, sentence := range acc.feed(text) {
					speak(sentence)
				}
			}

			if parts := extractGeminiFunctionCallParts(chunk); len(parts) > 0 {
				if firstTokenAt.IsZero() {
					firstTokenAt = time.Now()
					logger.Debugf(ctx, "Gemini stream iter=%d first tool call chunk TTFT=%.0fms", iteration, float64(time.Since(iterStart).Milliseconds()))
				}
				funcCallParts = append(funcCallParts, parts...)
			}
		}

		if err := scanner.Err(); err != nil {
			logger.Errorf(ctx, "Gemini streaming error: %v", err)
		}
		resp.Body.Close()

		streamDuration := time.Since(iterStart)
		logger.Debugf(ctx, "Gemini stream iter=%d done: chunks=%d thought=%d tools=%d total=%.0fms",
			iteration, chunkCount, thoughtChunks, len(funcCallParts), float64(streamDuration.Milliseconds()))

		if remainder := acc.drain(); remainder != "" {
			speak(remainder)
		}

		// No tool calls — we're done.
		if len(funcCallParts) == 0 {
			break
		}

		// Execute tool calls and append results so the next streaming iteration
		// gets the full tool context without a slow non-streaming round trip.
		var modelParts []map[string]interface{}
		var toolResponseParts []map[string]interface{}
		for _, part := range funcCallParts {
			fc, _ := part["functionCall"].(map[string]interface{})
			toolName, _ := fc["name"].(string)
			toolArgs, _ := fc["args"].(map[string]interface{})
			modelParts = append(modelParts, part) // full part preserves thought_signature

			toolStart := time.Now()
			logger.Debugf(ctx, "Gemini tool call: %s args=%v", toolName, toolArgs)
			toolResult, err := mcpManager.CallTool(ctx, client, toolName, toolArgs)
			if err != nil {
				toolResult = fmt.Sprintf("Error: %v", err)
			}
			logger.Debugf(ctx, "Tool %s result (%.0fms): %s", toolName, float64(time.Since(toolStart).Milliseconds()), toolResult)

			toolResponseParts = append(toolResponseParts, map[string]interface{}{
				"functionResponse": map[string]interface{}{
					"name":     toolName,
					"response": map[string]interface{}{"result": toolResult},
				},
			})
		}
		contents = append(contents,
			map[string]interface{}{"role": "model", "parts": modelParts},
			map[string]interface{}{"role": "function", "parts": toolResponseParts},
		)
	}

	// Drain the TTS pipeline and wait for all audio to finish playing.
	pipeline.close()

	response := strings.TrimSpace(assembled.String())
	if response != "" {
		logger.Infof(ctx, "Gemini sentence-stream response: %s", response)
	}
	return response
}
