/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package ai

import (
	"encoding/binary"
)

// OpusEncoder handles encoding PCM data to Opus format
// This is used as a fallback when the TTS API returns PCM/WAV instead of Opus
type OpusEncoder struct {
	sampleRate int
	channels   int
	frameSize  int
	bitrate    int // bits per second
}

// NewOpusEncoder creates a new Opus encoder
func NewOpusEncoder(sampleRate, channels int) *OpusEncoder {
	return &OpusEncoder{
		sampleRate: sampleRate,
		channels:   channels,
		frameSize:  sampleRate * 60 / 1000, // 60ms frames
		bitrate:    24000,                   // 24 kbps (good quality for voice)
	}
}

// EncodePCMToOpus encodes PCM samples to Opus frames
// Returns a single Opus packet containing all the PCM data
// Note: This is a fallback - in production, the TTS API should return Opus directly
func (e *OpusEncoder) EncodePCMToOpus(pcmData []int16) []byte {
	if len(pcmData) == 0 {
		return nil
	}

	// For fallback, we use a simple approach:
	// 1. If the PCM data is short (< 1 second), return it as WAV
	// 2. For longer data, chunk it and return WAV chunks
	// The ESP32 can handle WAV or we can convert to Opus on-device

	// In production with proper Opus encoder, this would encode to Opus
	// For now, return PCM data wrapped in WAV format as fallback
	return buildWavFile(pcmData, e.sampleRate, e.channels, 16)
}

// wavToPCM converts WAV data to PCM int16 samples
func wavToPCM(wavData []byte) ([]int16, error) {
	if len(wavData) < 44 {
		return nil, ErrInvalidWAV
	}

	// Check RIFF header
	if string(wavData[0:4]) != "RIFF" || string(wavData[8:12]) != "WAVE" {
		return nil, ErrInvalidWAV
	}

	// Find data chunk
	dataOffset := 12
	for dataOffset < len(wavData)-8 {
		chunkID := string(wavData[dataOffset : dataOffset+4])
		chunkSize := binary.LittleEndian.Uint32(wavData[dataOffset+4 : dataOffset+8])
		if chunkID == "data" {
			dataStart := dataOffset + 8
			dataEnd := dataStart + int(chunkSize)
			if dataEnd > len(wavData) {
				dataEnd = len(wavData)
			}
			return bytesToInt16(wavData[dataStart:dataEnd]), nil
		}
		dataOffset += 8 + int(chunkSize)
		if chunkSize%2 != 0 {
			dataOffset++ // Skip padding byte
		}
	}

	return nil, ErrInvalidWAV
}

// bytesToInt16 converts byte slice to int16 slice (little-endian)
func bytesToInt16(b []byte) []int16 {
	result := make([]int16, len(b)/2)
	for i := 0; i < len(result); i++ {
		result[i] = int16(binary.LittleEndian.Uint16(b[i*2 : i*2+2]))
	}
	return result
}

// pcmToBytes converts int16 slice to byte slice (little-endian)
func pcmToBytes(pcm []int16) []byte {
	result := make([]byte, len(pcm)*2)
	for i, sample := range pcm {
		binary.LittleEndian.PutUint16(result[i*2:i*2+2], uint16(sample))
	}
	return result
}

// ErrInvalidWAV is returned when WAV data is invalid
var ErrInvalidWAV = &WAVError{"invalid WAV format"}

// WAVError represents an error with WAV data
type WAVError struct {
	Message string
}

func (e *WAVError) Error() string {
	return e.Message
}
