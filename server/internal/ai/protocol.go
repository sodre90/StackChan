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
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gorilla/websocket"
	"github.com/hraban/opus"
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
	opusFrameDelayMs   = 20              // ms between Opus frames (frames are 60ms; lower than frame duration to build device-side buffer and avoid playback stutter)

	// VAD — inline RMS-based detector in processASRAndLLM.
	// speechPreBuffer: packets kept before detected onset to avoid clipping first phoneme.
	// RMS threshold and silence duration come from config (VADRMSThreshold, VADSilenceTimeoutMs).
	speechPreBuffer = 5 // 5 × 60ms = 300ms pre-buffer

	// Timing constants
	vadMaxListenDuration = 20 * time.Second       // hard ceiling — process regardless of VAD
	echoHoldoffDuration  = 1500 * time.Millisecond // ignore mic audio this long after TTS ends
	ttsRecoveryCooldown  = 12 * time.Second        // minimum gap between empty-ASR TTS resets

	// Retry settings
	maxRetries       = 3
	retryBaseDelayMs = 500
)

var (
	logger = g.Log()

	// WebSocket upgrader for AI protocol
	aiWSUpGrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	// AI configuration (set by Initialize)
	aiConfig Config

	// MCP tool manager (set by Initialize)
	mcpManager *MCPManager

	// Active clients registry
	clientsMu     sync.RWMutex
	activeClients = make(map[string]*AIClient)
)

// AIClient represents a connected ESP32 device for AI interaction
type AIClient struct {
	Mac         string
	Conn        *websocket.Conn
	mu          *sync.RWMutex
	writeMu     sync.Mutex // serialises all WebSocket writes (gorilla requires exclusive writer)
	SessionID   string
	LastTime    time.Time
	ctx         context.Context
	cancel      context.CancelFunc

	// Audio processing — each element is one Opus packet from the device
	opusPackets [][]byte
	isListening bool
	listenDone  chan struct{}  // closed when device sends listen stop
	decoder     *opus.Decoder // hraban/opus CGO decoder
	ttsEndedAt  time.Time     // when the last TTS playback finished (echo holdoff)

	// Speaking cancellation — cancelled when device sends abort
	speakCancel context.CancelFunc

	// TTS recovery cooldown
	lastRecoveryAt time.Time

	// Conversation context
	messages      []map[string]interface{}
	contextMu     sync.RWMutex
}

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
	mcpManager = NewMCPManager(config.BraveSearchAPIKey)
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
		"vad_ticker_interval_ms", config.VADTickerIntervalMs,
		"vad_rms_threshold", config.VADRMSThreshold,
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
		Mac:      mac,
		Conn:     ws,
		mu:       &sync.RWMutex{},
		ctx:      ctx,
		LastTime: time.Now(),
	}
	client.ctx, client.cancel = context.WithCancel(ctx)

	// Initialize Opus decoder (CGO-backed libopus, supports all modes)
	decoder, err := opus.NewDecoder(OpusSampleRate, OpusChannels)
	if err != nil {
		logger.Errorf(ctx, "Failed to init Opus decoder: %v", err)
		ws.Close()
		return
	}
	client.decoder = decoder

	// Register client
	clientsMu.Lock()
	activeClients[mac] = client
	clientsMu.Unlock()

	// Register device with MCP manager — pass the client's serialised write function
	// so MCP tool writes share the same writeMu and never race with sendJSON/sendAudioChunks.
	mcpManager.RegisterDevice(mac, client.writeWS)

	logger.Info(ctx, "AI client connected", "mac", mac)
	defer func() {
		clientsMu.Lock()
		delete(activeClients, mac)
		clientsMu.Unlock()
		mcpManager.MarkDeviceOffline(mac)
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

	if !client.isListening {
		return
	}

	// Discard audio during echo holdoff window — speaker echo for echoHoldoffDuration after TTS
	if !client.ttsEndedAt.IsZero() && time.Since(client.ttsEndedAt) < echoHoldoffDuration {
		return
	}

	totalBytes := 0
	for _, p := range client.opusPackets {
		totalBytes += len(p)
	}
	if totalBytes+len(msg) > maxAudioBufferSize {
		logger.Warning(ctx, "Audio buffer overflow, resetting")
		client.opusPackets = nil
		return
	}
	pkt := make([]byte, len(msg))
	copy(pkt, msg)
	client.opusPackets = append(client.opusPackets, pkt)
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
		client.opusPackets = nil
		client.listenDone = make(chan struct{})
		client.mu.Unlock()
		logger.Info(ctx, "Listening started", "mode", mode)

		// Start ASR processing in background
		go processASRAndLLM(ctx, client, mode)

	case "stop":
		client.isListening = false
		done := client.listenDone
		client.mu.Unlock()
		logger.Info(ctx, "Listening stopped")
		// Signal the processASRAndLLM goroutine to process immediately
		if done != nil {
			select {
			case <-done: // already closed
			default:
				close(done)
			}
		}

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
	logger.Info(ctx, "Speaking aborted by device", "reason", reason)

	client.mu.Lock()
	cancel := client.speakCancel
	client.speakCancel = nil
	client.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// processASRAndLLM handles the ASR -> LLM pipeline with VAD
func processASRAndLLM(ctx context.Context, client *AIClient, mode string) {
	client.mu.RLock()
	listenDone := client.listenDone
	client.mu.RUnlock()

	// Server-side VAD: decode new packets every vadTickerInterval and detect end-of-speech.
	// Closes listenDone (same channel handleListen("stop") uses) when silence follows speech.
	// vadMaxListenDuration is a hard ceiling in case the device never goes quiet.
	vadDecoder, err := opus.NewDecoder(OpusSampleRate, OpusChannels)
	if err != nil {
		logger.Errorf(ctx, "Failed to create VAD Opus decoder: %v", err)
		return
	}
	pcmBuf := make([]int16, 5760)
	var seenSpeech bool
	var lastSpeechAt time.Time
	var vadPktIdx int
	speechStartIdx := -1 // packet index where voice onset was first detected (-1 = not yet)

	silenceDuration := time.Duration(aiConfig.VADSilenceTimeoutMs) * time.Millisecond
	if silenceDuration <= 0 {
		silenceDuration = 800 * time.Millisecond
	}

	// VAD ticker interval: how often to scan for new audio packets.
	// Shorter = faster detection but more CPU; longer = smoother but higher latency.
	vadInterval := time.Duration(aiConfig.VADTickerIntervalMs) * time.Millisecond
	if vadInterval <= 0 {
		vadInterval = 100 * time.Millisecond
	}

	vadTicker := time.NewTicker(vadInterval)
	defer vadTicker.Stop()
	maxDuration := time.NewTimer(vadMaxListenDuration)
	defer maxDuration.Stop()

vadLoop:
	for {
		select {
		case <-listenDone:
			break vadLoop
		case <-maxDuration.C:
			logger.Debugf(ctx, "Max listen duration reached, processing accumulated audio")
			break vadLoop
		case <-vadTicker.C:
			client.mu.RLock()
			allPkts := client.opusPackets
			client.mu.RUnlock()

			if vadPktIdx > len(allPkts) {
				vadPktIdx = len(allPkts)
			}
			prevSpeechStartIdx := speechStartIdx
			maxRmsInTick := 0.0
			for i, pkt := range allPkts[vadPktIdx:] {
				n, err := vadDecoder.Decode(pkt, pcmBuf)
				if err != nil || n == 0 {
					continue
				}
				rms := calculateRMS(pcmBuf[:n])
				if rms > maxRmsInTick {
					maxRmsInTick = rms
				}
				if rms > aiConfig.VADRMSThreshold {
					if speechStartIdx < 0 {
						speechStartIdx = vadPktIdx + i // record onset
					}
					seenSpeech = true
					lastSpeechAt = time.Now()
				}
			}
			vadPktIdx = len(allPkts)

			// Speech just started this tick — tell the device to show a listening face
			if speechStartIdx >= 0 && prevSpeechStartIdx < 0 {
				sendLLM(ctx, client, "thinking", "")
			}

			// Log max RMS each tick for debugging VAD sensitivity
			if maxRmsInTick > 0 {
				logger.Debugf(ctx, "Server VAD tick: maxRMS=%.4f threshold=%.4f seenSpeech=%v silence=%.0fms", maxRmsInTick, aiConfig.VADRMSThreshold, seenSpeech, time.Since(lastSpeechAt).Milliseconds())
			}

			if seenSpeech && !lastSpeechAt.IsZero() && time.Since(lastSpeechAt) >= silenceDuration {
				logger.Debugf(ctx, "Server VAD: speech ended (%.0fms silence), triggering ASR", time.Since(lastSpeechAt).Seconds()*1000)
				client.mu.RLock()
				done := client.listenDone
				client.mu.RUnlock()
				if done != nil {
					select {
					case <-done:
					default:
						close(done)
					}
				}
				break vadLoop
			}
		case <-client.ctx.Done():
			return
		}
	}

	client.mu.Lock()
	packets := client.opusPackets
	client.opusPackets = nil
	client.isListening = false
	client.mu.Unlock()

	if len(packets) == 0 {
		logger.Debugf(ctx, "Packets already consumed by concurrent handler — skipping ASR")
		return
	}

	if !seenSpeech {
		logger.Debugf(ctx, "No speech detected by VAD, skipping ASR (%d packets discarded)", len(packets))
		return
	}

	// Trim leading silence: start from a few frames before speech onset so we
	// don't clip the first phoneme (5 frames × 60ms = 300ms pre-buffer).
	if speechStartIdx > speechPreBuffer {
		startIdx := speechStartIdx - speechPreBuffer
		if startIdx >= len(packets) {
			startIdx = 0
		}
		logger.Debugf(ctx, "Trimming %d leading-silence packets (speech onset at packet %d)", startIdx, speechStartIdx)
		packets = packets[startIdx:]
	}

	totalBytes := 0
	for _, p := range packets {
		totalBytes += len(p)
	}
	logger.Infof(ctx, "Processing %d Opus packets (%d bytes)", len(packets), totalBytes)

	// If ASR is enabled, transcribe the audio
	transcribedText := ""
	if aiConfig.EnableASR {
		transcribedText = transcribeAudio(ctx, client, packets)
	}

	if transcribedText == "" {
		// Device sent listen stop but ASR found no speech (silence or unrecognised audio).
		// Wait briefly — if a new listen cycle already started, don't interfere.
		time.Sleep(300 * time.Millisecond)
		client.mu.RLock()
		alreadyListening := client.isListening
		lastRecovery := client.lastRecoveryAt
		client.mu.RUnlock()
		if alreadyListening {
			logger.Warning(ctx, "ASR empty but new listen cycle already started, skipping reset")
			return
		}
		// Guard against rapid TTS recovery cycling
		if !lastRecovery.IsZero() && time.Since(lastRecovery) < ttsRecoveryCooldown {
			logger.Debugf(ctx, "Skipping TTS recovery, last was %v ago", time.Since(lastRecovery).Round(time.Millisecond))
			return
		}
		// Device is waiting for TTS — send empty cycle to unblock it
		logger.Warning(ctx, "ASR returned empty text, cycling TTS state to unblock device")
		client.mu.Lock()
		client.lastRecoveryAt = time.Now()
		client.mu.Unlock()
		sendTTS(ctx, client, "start", "")
		sendTTS(ctx, client, "stop", "")
		return
	}

	logger.Infof(ctx, "ASR transcribed: %q", transcribedText)

	// Send STT result to device
	sendSTT(ctx, client, transcribedText)

	// Process with LLM
	go processLLMResponse(ctx, client, transcribedText)
}

// processLLMResponse handles the LLM -> TTS pipeline with sentence-level streaming.
// For streaming LLM without tools, TTS fires per sentence as tokens arrive.
// For tools / non-streaming, the full response is split into sentences after the fact.
func processLLMResponse(ctx context.Context, client *AIClient, userText string) {
	addMessageToContext(ctx, client, "user", userText)

	// Create a speak context so handleAbort can cancel mid-playback.
	speakCtx, speakCancel := context.WithCancel(ctx)
	client.mu.Lock()
	client.speakCancel = speakCancel
	client.mu.Unlock()
	defer func() {
		speakCancel()
		client.mu.Lock()
		client.speakCancel = nil
		client.mu.Unlock()
	}()

	sendTTS(speakCtx, client, "start", "")

	var fullResponse string

	if !aiConfig.EnableMCPTools && aiConfig.StreamLLM {
		// Sentence-streaming: LLM tokens feed into the accumulator; TTS fires per sentence.
		if aiConfig.LLMProvider == "gemini" {
			fullResponse = streamLLMSentencesGemini(speakCtx, client)
		} else {
			fullResponse = streamLLMSentences(speakCtx, client)
		}
	} else {
		// Tools or non-streaming: get the complete response, then speak sentence by sentence.
		fullResponse = callLLM(speakCtx, client)
		fullResponse = stripEmojis(fullResponse)
		for _, sentence := range splitSentences(fullResponse) {
			if speakCtx.Err() != nil {
				break
			}
			sendTTS(speakCtx, client, "sentence_start", sentence)
			if aiConfig.EnableTTS {
				if audio := generateSpeech(speakCtx, sentence); len(audio) > 0 {
					sendAudioChunks(speakCtx, client, audio)
				}
			}
		}
	}

	if fullResponse == "" {
		sendTTS(ctx, client, "stop", "")
		return
	}

	addMessageToContext(ctx, client, "assistant", fullResponse)

	client.mu.Lock()
	client.ttsEndedAt = time.Now()
	client.mu.Unlock()

	sendTTS(ctx, client, "stop", "")
}

// streamLLMSentences streams the LLM response and calls TTS for each sentence as it completes.
// Returns the full assembled response text.
func streamLLMSentences(ctx context.Context, client *AIClient) string {
	systemPrompt := aiConfig.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "You are a helpful AI assistant."
	}

	contextMessages := getContextMessages(ctx, client)
	messages := []map[string]interface{}{{"role": "system", "content": systemPrompt}}
	messages = append(messages, contextMessages...)

	requestBody := map[string]interface{}{
		"model":       aiConfig.LLMModel,
		"messages":    messages,
		"temperature": 0.7,
		"max_tokens":  512,
		"stream":      true,
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
	resp, err := httpClient.Do(req)
	if err != nil {
		logger.Errorf(ctx, "LLM request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logger.Errorf(ctx, "LLM API error (status %d): %s", resp.StatusCode, string(body))
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
		delta, ok := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
		if !ok {
			continue
		}
		token, _ := delta["content"].(string)
		if token == "" {
			continue
		}
		for _, sentence := range acc.feed(token) {
			speak(sentence)
		}
	}
	if err := scanner.Err(); err != nil {
		logger.Errorf(ctx, "LLM streaming error: %v", err)
	}

	// Speak any trailing text that didn't end with sentence-ending punctuation
	if remainder := acc.drain(); remainder != "" {
		speak(remainder)
	}

	response := strings.TrimSpace(assembled.String())
	if response != "" {
		logger.Infof(ctx, "LLM sentence-stream response: %s", response)
	}
	return response
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

// transcribeAudio decodes Opus packets and sends PCM audio to the ASR service
func transcribeAudio(ctx context.Context, client *AIClient, packets [][]byte) string {
	asrURL := aiConfig.ASRBaseURL
	if asrURL == "" {
		asrURL = aiConfig.APIBaseURL
	}
	if asrURL == "" {
		logger.Warning(ctx, "ASR API base URL not configured")
		return ""
	}

	// Decode each Opus packet individually and concatenate PCM
	pcmData, err := decodeOpusPackets(client.decoder, packets)
	if err != nil {
		logger.Errorf(ctx, "Failed to decode Opus: %v", err)
		return ""
	}

	if len(pcmData) == 0 {
		logger.Warning(ctx, "Decoded PCM data is empty")
		return ""
	}

	totalBytes := 0
	for _, p := range packets {
		totalBytes += len(p)
	}
	logger.Infof(ctx, "Decoded %d Opus packets to %d PCM samples", len(packets), len(pcmData))

	// Build WAV file from PCM
	wavData := buildWavFile(pcmData, OpusSampleRate, OpusChannels, 16)

	// Send to ASR — only retry on transient HTTP errors, not on empty transcription
	for attempt := 0; attempt < maxRetries; attempt++ {
		text, err := callASRAPI(ctx, wavData, asrURL)
		if err == nil {
			return text // empty string means no speech detected — caller handles this
		}
		if attempt < maxRetries-1 {
			delay := time.Duration(retryBaseDelayMs*(1<<attempt)) * time.Millisecond
			logger.Infof(ctx, "ASR request error, retrying in %v (attempt %d/%d): %v", delay, attempt+1, maxRetries, err)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ""
			}
		} else {
			logger.Errorf(ctx, "ASR request failed after %d attempts: %v", maxRetries, err)
		}
	}
	return ""
}

// callASRAPI sends PCM/WAV data to the ASR API.
// Returns (text, nil) on success (text may be empty if no speech detected).
// Returns ("", err) on transient errors that warrant a retry.
func callASRAPI(ctx context.Context, wavData []byte, asrURL string) (string, error) {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	mw.WriteField("model", aiConfig.ASRModel)
	if aiConfig.ASRLanguage != "" && aiConfig.ASRLanguage != "auto" {
		mw.WriteField("language", aiConfig.ASRLanguage)
	}
	if aiConfig.ASRInitialPrompt != "" {
		mw.WriteField("prompt", aiConfig.ASRInitialPrompt)
	}
	part, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(wavData); err != nil {
		return "", fmt.Errorf("write WAV: %w", err)
	}
	mw.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", asrURL+"/audio/transcriptions", body)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	// Whisper server always returns JSON — extract text field
	var asrResp struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(bodyBytes, &asrResp); err == nil {
		text := strings.TrimSpace(asrResp.Text)
		if text == "" {
			logger.Debugf(ctx, "ASR: no speech detected in audio")
		}
		return text, nil
	}

	// Fallback: plain text response
	return strings.TrimSpace(string(bodyBytes)), nil
}

// decodeOpusPackets decodes a slice of individual Opus packets to PCM samples.
// Each element is exactly one Opus packet as received in a WebSocket binary message.
func decodeOpusPackets(decoder *opus.Decoder, packets [][]byte) ([]int16, error) {
	// Max frame size: 120ms at 48kHz = 5760 samples; use generously large buffer
	pcmBuf := make([]int16, 5760*OpusChannels)
	var allPCM []int16

	for i, pkt := range packets {
		if len(pkt) == 0 {
			continue
		}
		n, err := decoder.Decode(pkt, pcmBuf)
		if err != nil {
			logger.Warningf(context.Background(), "Skipping malformed Opus packet %d: %v", i, err)
			continue
		}
		allPCM = append(allPCM, pcmBuf[:n*OpusChannels]...)
	}

	if len(allPCM) == 0 {
		return nil, fmt.Errorf("no valid Opus packets decoded")
	}
	return allPCM, nil
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
	if aiConfig.LLMProvider == "gemini" {
		return callLLMGemini(ctx, client)
	}

	if aiConfig.APIBaseURL == "" {
		logger.Warning(ctx, "LLM API base URL not configured")
		return ""
	}

	// When MCP tools are enabled, use the tool-calling loop (non-streaming)
	if aiConfig.EnableMCPTools && mcpManager != nil {
		return callLLMWithTools(ctx, client)
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

// callLLMWithTools runs the OpenAI function-calling loop: sends tools, executes any
// tool calls the model makes, then returns the final text response.
// Always uses non-streaming so tool_calls can be parsed from the complete response.
func callLLMWithTools(ctx context.Context, client *AIClient) string {
	if aiConfig.APIBaseURL == "" {
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

	tools := mcpManager.GetToolDefinitions()
	httpClient := &http.Client{Timeout: 60 * time.Second}

	for iteration := 0; iteration < 5; iteration++ {
		requestBody := map[string]interface{}{
			"model":       aiConfig.LLMModel,
			"messages":    messages,
			"temperature": 0.7,
			"max_tokens":  512,
			"stream":      false,
			"tools":       tools,
			"tool_choice": "auto",
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

		resp, err := httpClient.Do(req)
		if err != nil {
			logger.Errorf(ctx, "LLM request failed: %v", err)
			return ""
		}
		responseBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			logger.Errorf(ctx, "Failed to read LLM response: %v", err)
			return ""
		}
		if resp.StatusCode != http.StatusOK {
			logger.Errorf(ctx, "LLM API error (status %d): %s", resp.StatusCode, string(responseBytes))
			return ""
		}

		var result map[string]interface{}
		if err := json.Unmarshal(responseBytes, &result); err != nil {
			logger.Errorf(ctx, "Failed to parse LLM response: %v", err)
			return ""
		}

		choices, ok := result["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			return ""
		}
		firstChoice, ok := choices[0].(map[string]interface{})
		if !ok {
			return ""
		}
		message, ok := firstChoice["message"].(map[string]interface{})
		if !ok {
			return ""
		}
		finishReason, _ := firstChoice["finish_reason"].(string)

		// If model wants to call tools, execute them and loop
		if finishReason == "tool_calls" || message["tool_calls"] != nil {
			toolCallsRaw, ok := message["tool_calls"].([]interface{})
			if !ok || len(toolCallsRaw) == 0 {
				break
			}

			// Append the assistant's tool-calling message
			messages = append(messages, message)

			for _, tcRaw := range toolCallsRaw {
				tc, ok := tcRaw.(map[string]interface{})
				if !ok {
					continue
				}
				callID, _ := tc["id"].(string)
				fn, _ := tc["function"].(map[string]interface{})
				toolName, _ := fn["name"].(string)
				argsStr, _ := fn["arguments"].(string)

				var toolArgs map[string]interface{}
				_ = json.Unmarshal([]byte(argsStr), &toolArgs)

				logger.Infof(ctx, "Tool call: %s args=%s", toolName, argsStr)
				toolResult, err := mcpManager.CallTool(ctx, client, toolName, toolArgs)
				if err != nil {
					toolResult = fmt.Sprintf("Error: %v", err)
				}
				logger.Infof(ctx, "Tool %s result: %s", toolName, toolResult)

				messages = append(messages, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": callID,
					"content":      toolResult,
				})
			}
			continue // send tool results back to LLM
		}

		// No tool calls — return the text content
		if content, ok := message["content"].(string); ok {
			response := strings.TrimSpace(content)
			if response != "" {
				logger.Infof(ctx, "LLM (tools) response: %s", response)
			}
			return response
		}
		break
	}
	return ""
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
	ttsBase := aiConfig.TTSBaseURL
	if ttsBase == "" {
		ttsBase = aiConfig.APIBaseURL
	}
	if ttsBase == "" {
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
		ttsBase+"/audio/speech", bytes.NewReader(bodyBytes))
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

// extractOpusFramesFromOGG parses an OGG container and returns individual Opus frames.
// The device expects one raw Opus frame per WebSocket binary message.
func extractOpusFramesFromOGG(data []byte) ([][]byte, error) {
	var frames [][]byte
	offset := 0
	pageNum := 0

	for offset+27 <= len(data) {
		if string(data[offset:offset+4]) != "OggS" {
			return nil, fmt.Errorf("invalid OGG sync at offset %d", offset)
		}

		numSegs := int(data[offset+26])
		if offset+27+numSegs > len(data) {
			break
		}

		segTable := data[offset+27 : offset+27+numSegs]
		dataStart := offset + 27 + numSegs

		dataLen := 0
		for _, s := range segTable {
			dataLen += int(s)
		}
		if dataStart+dataLen > len(data) {
			break
		}

		pageData := data[dataStart : dataStart+dataLen]
		pageNum++

		// Pages 1 and 2 are OpusHead and OpusTags headers — skip them
		if pageNum > 2 {
			var pkt []byte
			dataOff := 0
			for _, segSize := range segTable {
				end := dataOff + int(segSize)
				pkt = append(pkt, pageData[dataOff:end]...)
				dataOff = end
				if segSize < 255 {
					// Segment < 255 marks end of packet
					if len(pkt) > 0 {
						frame := make([]byte, len(pkt))
						copy(frame, pkt)
						frames = append(frames, frame)
						pkt = pkt[:0]
					}
				}
				// segSize == 255 means packet continues in next segment
			}
		}

		offset = dataStart + dataLen
	}

	return frames, nil
}

// sendAudioChunks sends TTS audio to the ESP32 as individual Opus frames.
// Auto-detects format: WAV (RIFF header) is encoded to Opus on the fly;
// OGG/Opus is demuxed directly. The ESP32 decoder requires one Opus packet per message.
func sendAudioChunks(ctx context.Context, client *AIClient, audioData []byte) {
	if len(audioData) == 0 {
		return
	}

	var frames [][]byte
	var err error

	if len(audioData) >= 4 && string(audioData[0:4]) == "RIFF" {
		// WAV input (e.g. from omlx) — resample to 16kHz and encode to Opus
		frames, err = wavToOpusFrames(audioData)
		if err != nil {
			logger.Errorf(ctx, "Failed to encode WAV to Opus: %v", err)
			return
		}
	} else {
		// OGG/Opus input (e.g. from edge-tts) — demux raw Opus frames
		frames, err = extractOpusFramesFromOGG(audioData)
		if err != nil {
			logger.Errorf(ctx, "Failed to parse OGG for playback: %v", err)
			return
		}
	}

	if len(frames) == 0 {
		logger.Warning(ctx, "No Opus frames extracted from TTS audio")
		return
	}

	totalFrames := len(frames)
	sentFrames := 0

	for _, frame := range frames {
		if ctx.Err() != nil {
			logger.Infof(ctx, "Audio playback interrupted after %d/%d frames", sentFrames, totalFrames)
			return
		}

		if err := client.writeWS(websocket.BinaryMessage, frame); err != nil {
			if err.Error() == "websocket connection is nil" {
				logger.Warning(ctx, "Client disconnected during audio playback")
			} else {
				logger.Errorf(ctx, "Failed to send audio frame %d/%d: %v", sentFrames+1, totalFrames, err)
				sendTTS(ctx, client, "abort", "connection error")
			}
			break
		}

		sentFrames++
		time.Sleep(opusFrameDelayMs * time.Millisecond)
	}

	if sentFrames > 0 {
		logger.Infof(ctx, "Audio playback complete: %d/%d frames sent", sentFrames, totalFrames)
	}
}

// writeWS sends a WebSocket message with exclusive write access.
// gorilla/websocket requires that no two goroutines write concurrently; writeMu
// enforces that across sendJSON, sendAudioChunks, and MCP tool writes.
func (c *AIClient) writeWS(messageType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.RLock()
	conn := c.Conn
	c.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("websocket connection is nil")
	}
	return conn.WriteMessage(messageType, data)
}

// sendJSON sends a JSON message to the device
func sendJSON(ctx context.Context, client *AIClient, data interface{}) {
	msg, err := json.Marshal(data)
	if err != nil {
		logger.Errorf(ctx, "Failed to marshal JSON: %v", err)
		return
	}

	if err = client.writeWS(websocket.TextMessage, msg); err != nil {
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

// stripEmojis removes emoji and pictograph characters from text.
// Keeps all standard Unicode text including accented Latin (Hungarian, etc.).
func stripEmojis(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 0x1F000: // Emoji, pictographs, supplemental symbols
		case r >= 0x2500 && r <= 0x2BFF: // Box drawing, misc symbols, dingbats
		case r >= 0xFE00 && r <= 0xFEFF: // Variation selectors, specials
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
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

// sentenceAccumulator buffers streaming LLM tokens and yields complete sentences.
type sentenceAccumulator struct {
	buf strings.Builder
}

// feed adds a token and returns any complete sentences now available.
func (sa *sentenceAccumulator) feed(token string) []string {
	sa.buf.WriteString(token)
	var out []string
	text := sa.buf.String()
	for {
		end := sentenceBoundary(text)
		if end < 0 {
			break
		}
		sentence := strings.TrimSpace(text[:end])
		text = strings.TrimLeft(text[end:], " \t\n\r")
		if len(sentence) >= 3 {
			out = append(out, sentence)
		}
	}
	sa.buf.Reset()
	sa.buf.WriteString(text)
	return out
}

// drain returns any remaining buffered text as a final (unpunctuated) sentence.
func (sa *sentenceAccumulator) drain() string {
	s := strings.TrimSpace(sa.buf.String())
	sa.buf.Reset()
	return s
}

// sentenceBoundary returns the index just past the first sentence-ending punctuation
// that is followed by whitespace or end-of-string. Returns -1 if none found.
// Consecutive punctuation (e.g. "..." or "!!") is treated as one boundary.
func sentenceBoundary(text string) int {
	for i := 0; i < len(text); i++ {
		b := text[i]
		if b != '.' && b != '!' && b != '?' {
			continue
		}
		end := i + 1
		for end < len(text) && (text[end] == '.' || text[end] == '!' || text[end] == '?') {
			end++
		}
		if end >= len(text) || text[end] == ' ' || text[end] == '\t' || text[end] == '\n' {
			return end
		}
		i = end - 1
	}
	return -1
}

// splitSentences splits a completed response string into individual sentences.
func splitSentences(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var sentences []string
	for {
		end := sentenceBoundary(text)
		if end < 0 {
			break
		}
		sentence := strings.TrimSpace(text[:end])
		text = strings.TrimLeft(text[end:], " \t\n\r")
		if len(sentence) >= 3 {
			sentences = append(sentences, sentence)
		}
	}
	if remainder := strings.TrimSpace(text); len(remainder) >= 3 {
		sentences = append(sentences, remainder)
	}
	if len(sentences) == 0 && len(text) >= 3 {
		return []string{text}
	}
	return sentences
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
