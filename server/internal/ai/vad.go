/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package ai

import (
	"math"
)

// VADConfig holds configuration for the voice activity detector
type VADConfig struct {
	// Energy-based thresholds
	SpeechThreshold float64 // RMS value above which audio is considered speech
	SilenceThreshold float64 // RMS value below which audio is considered silence

	// Timing parameters (in frames of 60ms)
	MinSpeechFrames      int // Minimum speech frames to trigger processing
	MinSilenceFrames     int // Minimum silence frames after speech
	MaxSilenceFrames     int // Maximum silence frames before giving up

	// Hysteresis to prevent rapid state changes
	ThresholdMargin float64 // Margin between speech and silence thresholds

	// Frame size in samples (for 16kHz audio, 60ms = 960 samples)
	FrameSize int
}

// DefaultVADConfig returns a default VAD configuration
func DefaultVADConfig() VADConfig {
	return VADConfig{
		SpeechThreshold:    0.015, // RMS normalized (0-1 range)
		SilenceThreshold:   0.005,
		MinSpeechFrames:    2,
		MinSilenceFrames:   15,
		MaxSilenceFrames:   100,
		ThresholdMargin:    0.005,
		FrameSize:          960, // 60ms at 16kHz
	}
}

// VADState represents the current state of the voice activity detector
type VADState int

const (
	VADIdle VADState = iota // No speech detected
	VADSpeaking             // Speech is being detected
	VADSilent               // Speech ended, waiting to process
)

// VAD implements a voice activity detector using energy-based detection
type VAD struct {
	config VADConfig
	state  VADState
	// Counters
	speechFrameCount  int
	silenceFrameCount int
	// Buffer for current frame
	frameBuffer []int16
}

// NewVAD creates a new voice activity detector
func NewVAD(config VADConfig) *VAD {
	return &VAD{
		config:      config,
		state:       VADIdle,
		frameBuffer: make([]int16, config.FrameSize),
	}
}

// ProcessFrame processes a single frame of audio and updates the VAD state
// Returns true if the state changed to a meaningful event
func (v *VAD) ProcessFrame(pcmData []int16) VADEvent {
	// Calculate RMS energy for this frame
	energy := calculateNormalizedRMS(pcmData)

	// Determine if this frame is speech or silence
	isSpeech := energy > v.config.SpeechThreshold
	isSilence := energy < v.config.SilenceThreshold

	var event VADEvent

	switch v.state {
	case VADIdle:
		if isSpeech {
			v.speechFrameCount++
			if v.speechFrameCount >= v.config.MinSpeechFrames {
				v.state = VADSpeaking
				event = VADEventSpeechStart
			}
		} else if isSilence {
			v.silenceFrameCount = 0
		}

	case VADSpeaking:
		if isSpeech {
			v.speechFrameCount++
			v.silenceFrameCount = 0
		} else if isSilence {
			v.silenceFrameCount++
			if v.silenceFrameCount >= v.config.MinSilenceFrames {
				v.state = VADSilent
				event = VADEventSilenceDetected
			}
		} else {
			// In-between energy, reset counters
			v.silenceFrameCount = 0
		}

	case VADSilent:
		if isSpeech {
			v.state = VADSpeaking
			event = VADEventSpeechRestart
		} else if v.silenceFrameCount >= v.config.MaxSilenceFrames {
			v.state = VADIdle
			v.speechFrameCount = 0
			v.silenceFrameCount = 0
			event = VADEventTimeout
		}
	}

	return event
}

// ProcessFrameAsync processes audio asynchronously and returns an event channel
func (v *VAD) ProcessFrameAsync(pcmData []int16) chan VADEvent {
	eventCh := make(chan VADEvent, 1)
	go func() {
		event := v.ProcessFrame(pcmData)
		eventCh <- event
		close(eventCh)
	}()
	return eventCh
}

// GetState returns the current VAD state
func (v *VAD) GetState() VADState {
	return v.state
}

// Reset resets the VAD to idle state
func (v *VAD) Reset() {
	v.state = VADIdle
	v.speechFrameCount = 0
	v.silenceFrameCount = 0
}

// VADEvent represents a VAD state change event
type VADEvent int

const (
	VADEventNone VADEvent = iota
	VADEventSpeechStart    // Speech has started (enough speech frames detected)
	VADEventSilenceDetected // Silence detected after speech
	VADEventSpeechRestart  // Speech restarted during silence period
	VADEventTimeout        // Max silence timeout reached
)

// String returns a string representation of the VAD event
func (e VADEvent) String() string {
	switch e {
	case VADEventNone:
		return "none"
	case VADEventSpeechStart:
		return "speech_start"
	case VADEventSilenceDetected:
		return "silence_detected"
	case VADEventSpeechRestart:
		return "speech_restart"
	case VADEventTimeout:
		return "timeout"
	default:
		return "unknown"
	}
}

// calculateNormalizedRMS calculates the Root Mean Square of PCM samples normalized to 0-1
func calculateNormalizedRMS(pcmData []int16) float64 {
	if len(pcmData) == 0 {
		return 0
	}

	var sumSquares float64
	for _, sample := range pcmData {
		// Normalize to -1 to 1 range
		val := float64(sample) / 32768.0
		sumSquares += val * val
	}

	return math.Sqrt(sumSquares / float64(len(pcmData)))
}

// calculateEnergy calculates the raw energy of a PCM frame
func calculateEnergy(pcmData []int16) float64 {
	if len(pcmData) == 0 {
		return 0
	}

	var energy float64
	for _, sample := range pcmData {
		val := float64(sample) / 32768.0
		energy += val * val
	}

	return energy / float64(len(pcmData))
}

// detectSpeech detects speech in PCM data using energy-based VAD
func detectSpeech(pcmData []int16, threshold float64) bool {
	energy := calculateEnergy(pcmData)
	return energy > threshold
}

// splitBySilence splits PCM data into speech segments separated by silence
func splitBySilence(pcmData []int16, silenceThreshold float64, minSpeechSamples int) [][]int16 {
	var segments [][]int16
	var currentSegment []int16
	isSpeaking := false

	frameSize := 320 // 20ms frames at 16kHz
	for i := 0; i < len(pcmData); i += frameSize {
		end := i + frameSize
		if end > len(pcmData) {
			end = len(pcmData)
		}
		frame := pcmData[i:end]

		if detectSpeech(frame, silenceThreshold) {
			if !isSpeaking && len(currentSegment) >= minSpeechSamples {
				// Start of new speech segment
				segments = append(segments, currentSegment)
				currentSegment = nil
			}
			isSpeaking = true
			currentSegment = append(currentSegment, frame...)
		} else {
			if isSpeaking && len(currentSegment) >= minSpeechSamples {
				// End of speech segment
				segments = append(segments, currentSegment)
				currentSegment = nil
			}
			isSpeaking = false
		}
	}

	// Don't forget the last segment
	if len(currentSegment) >= minSpeechSamples {
		segments = append(segments, currentSegment)
	}

	return segments
}

// TrimSilence removes leading and trailing silence from PCM data
func TrimSilence(pcmData []int16, silenceThreshold float64) []int16 {
	if len(pcmData) == 0 {
		return pcmData
	}

	// Find start of speech
	start := 0
	for start < len(pcmData) {
		frameEnd := start + 320
		if frameEnd > len(pcmData) {
			frameEnd = len(pcmData)
		}
		if detectSpeech(pcmData[start:frameEnd], silenceThreshold) {
			break
		}
		start++
	}

	// Find end of speech
	end := len(pcmData)
	for end > start {
		frameStart := end - 320
		if frameStart < start {
			frameStart = start
		}
		if detectSpeech(pcmData[frameStart:end], silenceThreshold) {
			break
		}
		end--
	}

	if start >= end {
		return nil
	}

	return pcmData[start:end]
}

// NormalizePCM normalizes PCM data to a target amplitude
func NormalizePCM(pcmData []int16, targetAmplitude float64) []int16 {
	if len(pcmData) == 0 || targetAmplitude <= 0 {
		return pcmData
	}

	// Find peak amplitude
	var peak float64
	for _, sample := range pcmData {
		val := math.Abs(float64(sample) / 32768.0)
		if val > peak {
			peak = val
		}
	}

	if peak == 0 {
		return pcmData
	}

	// Calculate gain
	gain := targetAmplitude / peak
	if gain > 1.0 {
		gain = 1.0 // Don't amplify beyond original
	}

	// Apply gain
	result := make([]int16, len(pcmData))
	for i, sample := range pcmData {
		val := float64(sample) * gain
		if val > 32767 {
			val = 32767
		} else if val < -32768 {
			val = -32768
		}
		result[i] = int16(val)
	}

	return result
}

// AdaptiveVAD implements a voice activity detector with adaptive thresholds
type AdaptiveVAD struct {
	VAD
	// Adaptive state
	meanEnergy  float64
	stdEnergy   float64
	sampleCount int
	windowSize  int
}

// NewAdaptiveVAD creates a new adaptive VAD
func NewAdaptiveVAD() *AdaptiveVAD {
	return &AdaptiveVAD{
		VAD:        *NewVAD(DefaultVADConfig()),
		windowSize: 50,
	}
}

// ProcessFrame processes a frame and adapts thresholds based on recent energy
func (av *AdaptiveVAD) ProcessFrame(pcmData []int16) VADEvent {
	energy := calculateNormalizedRMS(pcmData)

	// Update running statistics
	av.sampleCount++
	if av.sampleCount <= av.windowSize {
		av.meanEnergy = energy
		av.stdEnergy = 0
	} else {
		alpha := 0.01 // Learning rate
		oldMean := av.meanEnergy
		av.meanEnergy = alpha*energy + (1-alpha)*av.meanEnergy
		av.stdEnergy = alpha*math.Abs(energy-oldMean) + (1-alpha)*av.stdEnergy
	}

	// Set dynamic thresholds
	av.config.SpeechThreshold = av.meanEnergy + 2*av.stdEnergy + 0.002
	av.config.SilenceThreshold = av.meanEnergy - 0.001
	if av.config.SilenceThreshold < 0 {
		av.config.SilenceThreshold = 0
	}

	// Ensure minimum thresholds
	if av.config.SpeechThreshold < 0.005 {
		av.config.SpeechThreshold = 0.005
	}

	return av.VAD.ProcessFrame(pcmData)
}
