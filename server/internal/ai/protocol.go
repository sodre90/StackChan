/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gorilla/websocket"
	"github.com/pion/opus"
)

const (
	// Audio format constants
	OpusSampleRate    = 16000
	OpusChannels      = 1
	OpusFrameDuration = 60 // ms

	// Opus frame size: 60ms at 16kHz = 960 samples per channel
	opusFrameSamples = OpusSampleRate * OpusFrameDuration / 1000 // 960

	// Audio processing constants
	maxAudioBufferSize = 5 * 1024 * 1024 // 5MB max buffer
	opusChunkSize      = 2048            // bytes per chunk for sending to ESP32

	// VAD thresholds
	vadSpeechThreshold    = 800   // RMS threshold for speech detection
	vadSilenceThreshold   = 300   // RMS threshold for silence detection
	vadMinSpeechDuration  = 3     // Minimum frames of speech before triggering
	vadMinSilenceDuration = 25    // Minimum frames of silence before processing
	vadMaxSilenceDuration = 150   // Maximum silence frames before giving up

	// Retry settings
	maxRetries       = 3
	retryBaseDelayMs = 500

	// Control message types (matching the binary protocol)
	ControlAvatar byte = 0x03
	ControlMotion byte = 0x04
	Dance         byte = 0x14
)

var (
	logger = g.Log()

	// WebSocket upgrader for AI protocol
	aiWSUpGrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	// AI configuration (set by Initialize)
	aiConfig Config

	// Active clients registry
	clientsMu     sync.RWMutex
	activeClients = make(map[string]*AIClient)
)

// Reminder represents a scheduled reminder
// Note: This type is shared between protocol.go and mcp_tools.go
type Reminder struct {
	ID        string
	Message   string
	TriggerAt time.Time
	Active    bool
}

// AIClient represents a connected ESP32 device for AI interaction
type AIClient struct {
	Mac         string
	Conn        *websocket.Conn
	mu          *sync.RWMutex
	SessionID   string
	LastTime    time.Time
	ctx         context.Context
	cancel      context.CancelFunc

	// Audio processing
	audioBuffer   bytes.Buffer
	isListening   bool
	decoder       *opus.Decoder
	vadState      vadState
	vadFrameCount int

	// Conversation context
	messages      []map[string]interface{}
	contextMu     sync.RWMutex
}

// vadState tracks the VAD (Voice Activity Detection) state
type vadState int

const (
	vadIdle vadState = iota
	vadSpeaking
	vadSilent
)

// XiaoZhiHelloMessage is the hello message from the device
type XiaoZhiHelloMessage struct {
	Type       string            `json:"type"`
	Version    int               `json:"version"`
	Features   map[string]bool   `json:"features,omitempty"`
	Transport  string            `json:"transport"`
	AudioParam *AudioParams      `json:"audio_params,omitempty"`
	SessionID  string            `json:"session_id,omitempty"`
}

// AudioParams describes the audio format
type AudioParams struct {
	Format        string `json:"format"`
	SampleRate    int    `json:"sample_rate"`
	Channels      int    `json:"channels"`
	FrameDuration int    `json:"frame_duration,omitempty"`
}

// XiaoZhiListenMessage is the listen state message
type XiaoZhiListenMessage struct {
	SessionID string `json:"session_id,omitempty"`
	Type      string `json:"type"`
	State     string `json:"state"`
	Mode      string `json:"mode,omitempty"`
	Text      string `json:"text,omitempty"`
}

// XiaoZhiTTSMessage is the TTS state message
type XiaoZhiTTSMessage struct {
	SessionID string `json:"session_id,omitempty"`
	Type      string `json:"type"`
	State     string `json:"state"`
	Text      string `json:"text,omitempty"`
}

// XiaoZhiLLMMessage is the LLM emotion message
type XiaoZhiLLMMessage struct {
	SessionID string `json:"session_id,omitempty"`
	Type      string `json:"type"`
	Emotion   string `json:"emotion,omitempty"`
	Text      string `json:"text,omitempty"`
}

// XiaoZhiAbortMessage is the abort message
type XiaoZhiAbortMessage struct {
	SessionID string `json:"session_id,omitempty"`
	Type      string `json:"type"`
	Reason    string `json:"reason,omitempty"`
}

// Initialize sets up the AI protocol handler with the given configuration
func Initialize(config Config) {
	aiConfig = config
	logger.Info(context.Background(), "AI protocol initialized",
		"api_base_url", config.APIBaseURL,
		"llm_model", config.LLMModel,
		"asr_model", config.ASRModel,
		"tts_model", config.TTSModel,
		"tts_format", config.TTSResponseFormat,
		"stream_llm", config.StreamLLM,
		"enable_asr", config.EnableASR,
		"enable_tts", config.EnableTTS,
		"context_messages", config.ContextMessages,
		"vad_silence_timeout_ms", config.VADSilenceTimeoutMs,
	)
}

// Handler handles WebSocket connections for the XiaoZhi AI protocol
func Handler(r *ghttp.Request) {
	ctx := r.Context()

	// Get device MAC from header or query
	mac := r.Request.Header.Get("Device-Id")
	if mac == "" {
		mac = r.Get("mac").String()
	}
	if mac == "" {
		r.Response.WriteStatus(http.StatusBadRequest, "Device-Id header or mac parameter is required")
		return
	}

	ws, err := aiWSUpGrader.Upgrade(r.Response.Writer, r.Request, nil)
	if err != nil {
		logger.Errorf(ctx, "WebSocket upgrade failed: %v", err)
		return
	}

	client := &AIClient{
		Mac:        mac,
		Conn:       ws,
		mu:         &sync.RWMutex{},
		ctx:        ctx,
		LastTime:   time.Now(),
		vadState:   vadIdle,
		vadFrameCount: 0,
	}
	client.ctx, client.cancel = context.WithCancel(ctx)

	// Initialize Opus decoder
	decoder := opus.NewDecoder()
	if err := decoder.Init(OpusSampleRate, OpusChannels); err != nil {
		logger.Errorf(ctx, "Failed to init Opus decoder: %v", err)
		ws.Close()
		return
	}
	client.decoder = &decoder

	// Register client
	clientsMu.Lock()
	activeClients[mac] = client
	clientsMu.Unlock()

	logger.Info(ctx, "AI client connected", "mac", mac)
	defer func() {
		clientsMu.Lock()
		delete(activeClients, mac)
		clientsMu.Unlock()
		client.cancel()
		client.Conn.Close()
		logger.Info(ctx, "AI client disconnected", "mac", mac)
	}()

	// Start reading messages
	for {
		messageType, msg, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				break
			}
			logger.Errorf(ctx, "AI client read error: %v", err)
			break
		}

		client.LastTime = time.Now()

		if messageType == websocket.TextMessage {
			handleTextMessage(ctx, client, msg)
		} else if messageType == websocket.BinaryMessage {
			handleBinaryMessage(ctx, client, msg)
		}
	}
}

// handleTextMessage processes JSON messages from the device
func handleTextMessage(ctx context.Context, client *AIClient, msg []byte) {
	var envelope map[string]interface{}
	if err := json.Unmarshal(msg, &envelope); err != nil {
		logger.Warningf(ctx, "Failed to parse JSON message: %v", err)
		return
	}

	msgType, ok := envelope["type"].(string)
	if !ok {
		logger.Warning(ctx, "Missing message type")
		return
	}

	switch msgType {
	case "hello":
		handleHello(ctx, client, envelope)
	case "listen":
		handleListen(ctx, client, envelope)
	case "abort":
		handleAbort(ctx, client, envelope)
	default:
		logger.Infof(ctx, "Unknown message type: %s", msgType)
	}
}

// handleBinaryMessage processes Opus audio data from the device
func handleBinaryMessage(ctx context.Context, client *AIClient, msg []byte) {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.isListening {
		// Enforce max buffer size to prevent memory issues
		if client.audioBuffer.Len()+len(msg) > maxAudioBufferSize {
			logger.Warning(ctx, "Audio buffer overflow, resetting")
			client.audioBuffer.Reset()
			return
		}
		client.audioBuffer.Write(msg)
	}
}

// handleHello processes the hello handshake message
func handleHello(ctx context.Context, client *AIClient, envelope map[string]interface{}) {
	// Generate session ID
	client.SessionID = fmt.Sprintf("session_%s_%d", client.Mac, time.Now().UnixMilli())

	// Send hello response
	response := XiaoZhiHelloMessage{
		Type:      "hello",
		Transport: "websocket",
		SessionID: client.SessionID,
		AudioParam: &AudioParams{
			Format:        "opus",
			SampleRate:    OpusSampleRate,
			Channels:      OpusChannels,
			FrameDuration: OpusFrameDuration,
		},
	}

	sendJSON(ctx, client, response)
	logger.Info(ctx, "Hello handshake completed", "session_id", client.SessionID)
}

// handleListen processes the listen state change message
func handleListen(ctx context.Context, client *AIClient, envelope map[string]interface{}) {
	state, _ := envelope["state"].(string)
	mode, _ := envelope["mode"].(string)
	text, _ := envelope["text"].(string)

	client.mu.Lock()
	switch state {
	case "start":
		client.isListening = true
		client.audioBuffer.Reset()
		client.vadState = vadIdle
		client.vadFrameCount = 0
		client.mu.Unlock()
		logger.Info(ctx, "Listening started", "mode", mode)

		// Start ASR processing in background
		go processASRAndLLM(ctx, client, mode)

	case "stop":
		client.isListening = false
		client.mu.Unlock()
		logger.Info(ctx, "Listening stopped")

	case "detect":
		// Wake word detected - process the text directly
		client.mu.Unlock()
		if text != "" {
			go processLLMResponse(ctx, client, text)
		}
	}
}

// handleAbort processes the abort speaking message
func handleAbort(ctx context.Context, client *AIClient, envelope map[string]interface{}) {
	reason, _ := envelope["reason"].(string)
	logger.Info(ctx, "Speaking aborted", "reason", reason)
}

// processASRAndLLM handles the ASR -> LLM pipeline with VAD
func processASRAndLLM(ctx context.Context, client *AIClient, mode string) {
	// Wait for voice activity to stop using VAD
	silenceTimeout := time.Duration(aiConfig.VADSilenceTimeoutMs) * time.Millisecond

	// First, wait for the initial silence timeout to collect audio
	select {
	case <-time.After(silenceTimeout):
		// Timeout reached - process the audio
	case <-client.ctx.Done():
		return
	}

	client.mu.Lock()
	audioData := client.audioBuffer.Bytes()
	client.audioBuffer.Reset()
	client.isListening = false
	client.mu.Unlock()

	if len(audioData) == 0 {
		return
	}

	logger.Infof(ctx, "Processing %d bytes of Opus audio", len(audioData))

	// If ASR is enabled, transcribe the audio
	transcribedText := ""
	if aiConfig.EnableASR {
		transcribedText = transcribeAudio(ctx, client, audioData)
	}

	if transcribedText == "" {
		// No speech detected or ASR failed
		logger.Warning(ctx, "ASR returned empty text, skipping LLM")
		return
	}

	logger.Infof(ctx, "ASR transcribed: %q", transcribedText)

	// Send STT result to device
	sendSTT(ctx, client, transcribedText)

	// Process with LLM
	go processLLMResponse(ctx, client, transcribedText)
}

// processLLMResponse handles the LLM -> TTS pipeline
func processLLMResponse(ctx context.Context, client *AIClient, userText string) {
	// Add user message to context
	addMessageToContext(ctx, client, "user", userText)

	// Call LLM (with streaming support)
	response := callLLM(ctx, client)
	if response == "" {
		return
	}

	// Add assistant response to context
	addMessageToContext(ctx, client, "assistant", response)

	// Send TTS start
	sendTTS(ctx, client, "start", "")

	// Send the text for display
	sendTTS(ctx, client, "sentence_start", response)

	// If TTS is enabled, generate speech
	if aiConfig.EnableTTS {
		audioData := generateSpeech(ctx, response)
		if len(audioData) > 0 {
			// Send audio in chunks
			sendAudioChunks(ctx, client, audioData)
		}
	}

	// Send TTS stop
	sendTTS(ctx, client, "stop", "")
}

// addMessageToContext adds a message to the client's conversation context
func addMessageToContext(ctx context.Context, client *AIClient, role, content string) {
	client.contextMu.Lock()
	defer client.contextMu.Unlock()

	client.messages = append(client.messages, map[string]interface{}{
		"role":    role,
		"content": content,
	})

	// Trim context to configured size
	maxMsgs := aiConfig.ContextMessages
	if maxMsgs <= 0 {
		maxMsgs = 10
	}
	if len(client.messages) > maxMsgs*2 { // Each exchange is 2 messages
		client.messages = client.messages[len(client.messages)-maxMsgs*2:]
	}

	logger.Debugf(ctx, "Context now has %d messages", len(client.messages))
}

// getContextMessages returns the conversation context messages
func getContextMessages(ctx context.Context, client *AIClient) []map[string]interface{} {
	client.contextMu.RLock()
	defer client.contextMu.RUnlock()

	// Return a copy
	msgs := make([]map[string]interface{}, len(client.messages))
	copy(msgs, client.messages)
	return msgs
}

// transcribeAudio decodes Opus audio and sends it to the ASR service
func transcribeAudio(ctx context.Context, client *AIClient, opusData []byte) string {
	if aiConfig.APIBaseURL == "" {
		logger.Warning(ctx, "ASR API base URL not configured")
		return ""
	}

	// Decode Opus to PCM
	pcmData, err := decodeOpusToPCM(client.decoder, opusData)
	if err != nil {
		logger.Errorf(ctx, "Failed to decode Opus: %v", err)
		return ""
	}

	if len(pcmData) == 0 {
		logger.Warning(ctx, "Decoded PCM data is empty")
		return ""
	}

	logger.Infof(ctx, "Decoded %d bytes of Opus to %d PCM samples", len(opusData), len(pcmData)/2)

	// Build WAV file from PCM
	wavData := buildWavFile(pcmData, OpusSampleRate, OpusChannels, 16)

	// Send to ASR with retry
	var text string
	for attempt := 0; attempt < maxRetries; attempt++ {
		text = callASRAPI(ctx, wavData)
		if text != "" {
			return text
		}
		if attempt < maxRetries-1 {
			delay := time.Duration(retryBaseDelayMs*(1<<attempt)) * time.Millisecond
			logger.Infof(ctx, "ASR failed, retrying in %v (attempt %d/%d)", delay, attempt+1, maxRetries)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ""
			}
		}
	}
	return text
}

// callASRAPI sends PCM/WAV data to the ASR API
func callASRAPI(ctx context.Context, wavData []byte) string {
	// Prepare the request body
	body := &bytes.Buffer{}
	writer := bufio.NewWriter(body)
	writer.Write(wavData)
	writer.Flush()

	// Create the HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST",
		aiConfig.APIBaseURL+"/audio/transcriptions", body)
	if err != nil {
		logger.Errorf(ctx, "Failed to create ASR request: %v", err)
		return ""
	}

	req.Header.Set("Content-Type", "audio/wav")
	req.URL.RawQuery = fmt.Sprintf("model=%s&response_format=text", aiConfig.ASRModel)

	// Send the request
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		logger.Errorf(ctx, "ASR request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		logger.Errorf(ctx, "ASR API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		return ""
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf(ctx, "Failed to read ASR response: %v", err)
		return ""
	}

	text := strings.TrimSpace(string(bodyBytes))
	if text == "" {
		logger.Warning(ctx, "ASR returned empty response")
	}
	return text
}

// decodeOpusToPCM decodes Opus audio data to PCM samples (16-bit, little-endian)
func decodeOpusToPCM(decoder *opus.Decoder, opusData []byte) ([]int16, error) {
	// Parse Opus frames and decode each one
	var allPCM []int16
	frameSize := opusFrameSamples // 960 samples per frame

	// Try decoding the whole buffer as a single Opus frame first
	pcmBuf := make([]int16, frameSize)
	n, err := decoder.DecodeToInt16(opusData, pcmBuf)
	if err == nil && n > 0 {
		allPCM = append(allPCM, pcmBuf[:n]...)
		return allPCM, nil
	}

	// If single-frame decode failed, try frame-by-frame
	pos := 0
	for pos < len(opusData) {
		frameEnd := findOpusFrameEnd(opusData[pos:])
		if frameEnd <= 0 {
			break
		}

		frame := opusData[pos : pos+frameEnd]
		pcmBuf := make([]int16, frameSize)
		n, err := decoder.DecodeToInt16(frame, pcmBuf)
		if err != nil {
			logger.Warningf(context.Background(), "Failed to decode Opus frame at pos %d: %v", pos, err)
			pos += frameEnd
			continue
		}

		allPCM = append(allPCM, pcmBuf[:n]...)
		pos += frameEnd
	}

	if len(allPCM) == 0 {
		return nil, fmt.Errorf("no valid Opus frames decoded")
	}

	return allPCM, nil
}

// findOpusFrameEnd finds the end of the first valid Opus frame in the data
func findOpusFrameEnd(data []byte) int {
	if len(data) < 3 {
		return -1
	}

	// Try common frame sizes for 60ms Opus frames at 16kHz
	for size := 20; size <= 4000; size++ {
		if size > len(data) {
			return -1
		}
		toc := data[0]
		frameType := (toc >> 5) & 0x07
		if frameType == 3 {
			continue
		}
		return size
	}
	return -1
}

// buildWavFile creates a WAV file from PCM samples
func buildWavFile(pcmData []int16, sampleRate, channels, bitsPerSample int) []byte {
	numSamples := len(pcmData)
	dataSize := numSamples * (bitsPerSample / 8)
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8

	wav := make([]byte, 44+dataSize)

	// RIFF header
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(36+dataSize))
	copy(wav[8:12], "WAVE")

	// fmt subchunk
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(wav[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(wav[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(wav[32:34], uint16(blockAlign))
	binary.LittleEndian.PutUint16(wav[34:36], uint16(bitsPerSample))

	// data subchunk
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(dataSize))

	// Write PCM samples (little-endian)
	for i, sample := range pcmData {
		offset := 44 + i*(bitsPerSample/8)
		binary.LittleEndian.PutUint16(wav[offset:offset+2], uint16(sample))
	}

	return wav
}

// callLLM sends the transcribed text to the LLM and returns the response
func callLLM(ctx context.Context, client *AIClient) string {
	if aiConfig.APIBaseURL == "" {
		logger.Warning(ctx, "LLM API base URL not configured")
		return ""
	}

	systemPrompt := aiConfig.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "You are a helpful AI assistant."
	}

	contextMessages := getContextMessages(ctx, client)

	messages := []map[string]interface{}{
		{"role": "system", "content": systemPrompt},
	}
	messages = append(messages, contextMessages...)
	messages = append(messages, map[string]interface{}{
		"role":    "user",
		"content": "User just spoke.",
	})

	requestBody := map[string]interface{}{
		"model":       aiConfig.LLMModel,
		"messages":    messages,
		"temperature": 0.7,
		"max_tokens":  512,
		"stream":      aiConfig.StreamLLM,
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		logger.Errorf(ctx, "Failed to marshal LLM request: %v", err)
		return ""
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		aiConfig.APIBaseURL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		logger.Errorf(ctx, "Failed to create LLM request: %v", err)
		return ""
	}

	req.Header.Set("Content-Type", "application/json")
	if aiConfig.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+aiConfig.APIKey)
	}

	httpClient := &http.Client{Timeout: 60 * time.Second}

	if aiConfig.StreamLLM {
		return callLLMStream(ctx, req, httpClient)
	}

	return callLLMNonStream(ctx, req, httpClient)
}

// callLLMStream handles streaming LLM responses
func callLLMStream(ctx context.Context, req *http.Request, httpClient *http.Client) string {
	resp, err := httpClient.Do(req)
	if err != nil {
		logger.Errorf(ctx, "LLM request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		logger.Errorf(ctx, "LLM API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		return ""
	}

	var fullResponse strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		choices, ok := chunk["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}

		firstChoice, ok := choices[0].(map[string]interface{})
		if !ok {
			continue
		}

		delta, ok := firstChoice["delta"].(map[string]interface{})
		if !ok {
			continue
		}

		content, ok := delta["content"].(string)
		if !ok {
			continue
		}

		fullResponse.WriteString(content)
	}

	if err := scanner.Err(); err != nil {
		logger.Errorf(ctx, "LLM streaming error: %v", err)
	}

	response := strings.TrimSpace(fullResponse.String())
	if response != "" {
		logger.Infof(ctx, "LLM streaming response: %s", response)
	}
	return response
}

// callLLMNonStream handles non-streaming LLM responses
func callLLMNonStream(ctx context.Context, req *http.Request, httpClient *http.Client) string {
	resp, err := httpClient.Do(req)
	if err != nil {
		logger.Errorf(ctx, "LLM request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		logger.Errorf(ctx, "LLM API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		return ""
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf(ctx, "Failed to read LLM response: %v", err)
		return ""
	}

	var result map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		logger.Errorf(ctx, "Failed to parse LLM response: %v", err)
		return ""
	}

	choices, ok := result["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		logger.Warning(ctx, "No choices in LLM response")
		return ""
	}

	firstChoice, ok := choices[0].(map[string]interface{})
	if !ok {
		logger.Warning(ctx, "Invalid choice format in LLM response")
		return ""
	}

	message, ok := firstChoice["message"].(map[string]interface{})
	if !ok {
		logger.Warning(ctx, "Invalid message format in LLM response")
		return ""
	}

	if text, ok := message["content"].(string); ok {
		response := strings.TrimSpace(text)
		logger.Infof(ctx, "LLM response: %s", response)
		return response
	}

	return ""
}

// generateSpeech calls the TTS API to generate speech audio
func generateSpeech(ctx context.Context, text string) []byte {
	if aiConfig.APIBaseURL == "" {
		logger.Warning(ctx, "TTS API base URL not configured")
		return nil
	}

	requestBody := map[string]interface{}{
		"model":           aiConfig.TTSModel,
		"input":           text,
		"voice":           aiConfig.TTSVoice,
		"response_format": aiConfig.TTSResponseFormat,
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		logger.Errorf(ctx, "Failed to marshal TTS request: %v", err)
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		aiConfig.APIBaseURL+"/audio/speech", bytes.NewReader(bodyBytes))
	if err != nil {
		logger.Errorf(ctx, "Failed to create TTS request: %v", err)
		return nil
	}

	req.Header.Set("Content-Type", "application/json")
	if aiConfig.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+aiConfig.APIKey)
	}

	httpClient := &http.Client{Timeout: 60 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		logger.Errorf(ctx, "TTS request failed: %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		logger.Errorf(ctx, "TTS API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		return nil
	}

	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf(ctx, "Failed to read TTS response: %v", err)
		return nil
	}

	logger.Infof(ctx, "Generated TTS audio: %d bytes (format: %s) for text: %s",
		len(audioData), aiConfig.TTSResponseFormat, text)
	return audioData
}

// sendAudioChunks sends audio data to the device in chunks with error recovery
func sendAudioChunks(ctx context.Context, client *AIClient, audioData []byte) {
	if len(audioData) == 0 {
		return
	}

	totalChunks := (len(audioData) + opusChunkSize - 1) / opusChunkSize
	sentChunks := 0

	for i := 0; i < len(audioData); i += opusChunkSize {
		end := i + opusChunkSize
		if end > len(audioData) {
			end = len(audioData)
		}
		chunk := audioData[i:end]

		// Check if client is still connected
		client.mu.RLock()
		conn := client.Conn
		client.mu.RUnlock()

		if conn == nil {
			logger.Warning(ctx, "Client disconnected during audio playback")
			break
		}

		err := conn.WriteMessage(websocket.BinaryMessage, chunk)
		if err != nil {
			logger.Errorf(ctx, "Failed to send audio chunk %d/%d: %v", sentChunks+1, totalChunks, err)

			// Try to recover by sending abort message
			sendTTS(ctx, client, "abort", "connection error")
			break
		}

		sentChunks++

		// Small delay to simulate real-time playback
		// For Opus at 16kHz, ~130ms per 2KB chunk is appropriate
		time.Sleep(130 * time.Millisecond)
	}

	if sentChunks > 0 {
		logger.Infof(ctx, "Audio playback complete: %d/%d chunks sent", sentChunks, totalChunks)
	}
}

// sendJSON sends a JSON message to the device
func sendJSON(ctx context.Context, client *AIClient, data interface{}) {
	msg, err := json.Marshal(data)
	if err != nil {
		logger.Errorf(ctx, "Failed to marshal JSON: %v", err)
		return
	}

	client.mu.RLock()
	err = client.Conn.WriteMessage(websocket.TextMessage, msg)
	client.mu.RUnlock()

	if err != nil {
		logger.Errorf(ctx, "Failed to send JSON message: %v", err)
	}
}

// sendSTT sends the speech-to-text result to the device
func sendSTT(ctx context.Context, client *AIClient, text string) {
	msg := map[string]interface{}{
		"type":       "stt",
		"session_id": client.SessionID,
		"text":       text,
	}
	sendJSON(ctx, client, msg)
}

// sendTTS sends the TTS state message to the device
func sendTTS(ctx context.Context, client *AIClient, state, text string) {
	msg := map[string]interface{}{
		"type":       "tts",
		"session_id": client.SessionID,
		"state":      state,
	}
	if text != "" {
		msg["text"] = text
	}
	sendJSON(ctx, client, msg)
}

// sendAbort sends an abort message to stop current playback
func sendAbort(ctx context.Context, client *AIClient, reason string) {
	msg := map[string]interface{}{
		"type":       "abort",
		"session_id": client.SessionID,
		"reason":     reason,
	}
	sendJSON(ctx, client, msg)
}

// sendLLM sends the LLM emotion message to the device
func sendLLM(ctx context.Context, client *AIClient, emotion, text string) {
	msg := map[string]interface{}{
		"type":       "llm",
		"session_id": client.SessionID,
		"emotion":    emotion,
		"text":       text,
	}
	sendJSON(ctx, client, msg)
}

// GetActiveClients returns a list of currently connected AI clients
func GetActiveClients() []string {
	clientsMu.RLock()
	defer clientsMu.RUnlock()

	result := make([]string, 0, len(activeClients))
	for mac := range activeClients {
		result = append(result, mac)
	}
	return result
}

// calculateRMS calculates the Root Mean Square of PCM samples
func calculateRMS(pcmData []int16) float64 {
	if len(pcmData) == 0 {
		return 0
	}

	var sumSquares float64
	for _, sample := range pcmData {
		// Normalize to 0-1 range
		val := float64(sample) / 32768.0
		sumSquares += val * val
	}

	return math.Sqrt(sumSquares / float64(len(pcmData)))
}
