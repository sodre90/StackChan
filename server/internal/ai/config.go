/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package ai

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds the AI backend configuration for local OpenAI-compatible models
type Config struct {
	// OpenAI-compatible API base URL for LLM/TTS (e.g., http://localhost:11434/v1 for Ollama)
	APIBaseURL string `yaml:"api_base_url" json:"api_base_url"`

	// OpenAI-compatible API base URL for ASR (e.g., http://localhost:13000/v1 for Whisper server)
	// If empty, falls back to APIBaseURL
	ASRBaseURL string `yaml:"asr_base_url" json:"asr_base_url"`

	// OpenAI-compatible API base URL for TTS (e.g., http://localhost:14000/v1 for local TTS server)
	// If empty, falls back to APIBaseURL
	TTSBaseURL string `yaml:"tts_base_url" json:"tts_base_url"`

	// API key (may be empty for local models)
	APIKey string `yaml:"api_key" json:"api_key"`

	// ASR (Speech-to-Text) model name (e.g., "whisper-large-v3" for Ollama)
	ASRModel string `yaml:"asr_model" json:"asr_model"`

	// LLM model name (e.g., "qwen2.5", "llama3", "gpt-4o-mini")
	LLMModel string `yaml:"llm_model" json:"llm_model"`

	// TTS (Text-to-Speech) model name (e.g., "tts-1", "tts-1-hd")
	TTSModel string `yaml:"tts_model" json:"tts_model"`

	// TTS voice name (e.g., "alloy", "echo", "fable", "onyx", "nova", "shimmer")
	TTSVoice string `yaml:"tts_voice" json:"tts_voice"`

	// TTS response format (mp3, opus, aac, flac, wav, pcm)
	TTSResponseFormat string `yaml:"tts_response_format" json:"tts_response_format"`

	// System prompt for the LLM
	SystemPrompt string `yaml:"system_prompt" json:"system_prompt"`

	// Enable ASR (if false, device sends text directly)
	EnableASR bool `yaml:"enable_asr" json:"enable_asr"`

	// Enable TTS (if false, device receives text directly)
	EnableTTS bool `yaml:"enable_tts" json:"enable_tts"`

	// Conversation context: number of recent message pairs to keep
	ContextMessages int `yaml:"context_messages" json:"context_messages"`

	// LLM streaming: stream responses for lower latency
	StreamLLM bool `yaml:"stream_llm" json:"stream_llm"`

	// Voice activity detection: silence timeout in ms before processing speech
	VADSilenceTimeoutMs int `yaml:"vad_silence_timeout_ms" json:"vad_silence_timeout_ms"`

	// WebSocket port for the AI protocol handler
	// Set to 0 to use the main server port
	WSPort int `yaml:"ws_port" json:"ws_port"`

	// Enable MCP tools for robot control
	EnableMCPTools bool `yaml:"enable_mcp_tools" json:"enable_mcp_tools"`

	// ASR language hint (e.g., "hu", "en", "auto"). Empty means auto-detect.
	ASRLanguage string `yaml:"asr_language" json:"asr_language"`
}

// DefaultConfig returns the default AI configuration for local Ollama setup
func DefaultConfig() Config {
	return Config{
		APIBaseURL:          "http://localhost:11434/v1",
		ASRBaseURL:          "http://localhost:13000/v1",
		TTSBaseURL:          "http://localhost:14000/v1",
		APIKey:              "",
		ASRModel:            "whisper",
		LLMModel:            "qwen2.5",
		TTSModel:            "tts-1",
		TTSVoice:            "alloy",
		TTSResponseFormat:   "opus",
		SystemPrompt:        "You are StackChan, a cute AI desktop robot built on M5Stack CoreS3. You have a screen face with expressive eyes and a mouth. Be friendly, helpful, and concise in your responses. Keep responses short (under 100 words) since they are spoken aloud. Use a warm, playful tone.",
		EnableASR:           true,
		EnableTTS:           true,
		ContextMessages:     10,
		StreamLLM:           true,
		VADSilenceTimeoutMs: 1500,
		WSPort:              0,
		EnableMCPTools:      true,
	}
}

// LoadConfig loads AI configuration from a YAML file
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()

	// If no path specified, try default locations
	if path == "" {
		// Try current directory first
		if _, err := os.Stat("config.yaml"); err == nil {
			path = "config.yaml"
		} else if _, err := os.Stat("manifest/config/config.yaml"); err == nil {
			path = "manifest/config/config.yaml"
		} else {
			// Use defaults
			return cfg, nil
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}

	// Apply defaults for any zero values
	if cfg.TTSResponseFormat == "" {
		cfg.TTSResponseFormat = "opus"
	}
	if cfg.ContextMessages <= 0 {
		cfg.ContextMessages = 10
	}
	if cfg.VADSilenceTimeoutMs <= 0 {
		cfg.VADSilenceTimeoutMs = 1500
	}

	return cfg, nil
}

// LoadConfigFromDir loads AI configuration from a YAML file in the given directory
func LoadConfigFromDir(dir string) (Config, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return DefaultConfig(), err
	}

	path := filepath.Join(absDir, "config.yaml")
	return LoadConfig(path)
}
