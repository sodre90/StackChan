/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package ai

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/hraban/opus"
)

// ErrInvalidWAV is returned when WAV data cannot be parsed.
var ErrInvalidWAV = errors.New("invalid WAV format")

// parseWAV extracts PCM samples and audio metadata from a WAV file.
// Scans for both "fmt " and "data" chunks to support non-standard orderings.
func parseWAV(data []byte) (pcm []int16, sampleRate, channels int, err error) {
	if len(data) < 12 {
		return nil, 0, 0, ErrInvalidWAV
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, 0, ErrInvalidWAV
	}

	offset := 12
	var fmtFound bool

	for offset+8 <= len(data) {
		chunkID := string(data[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))

		switch chunkID {
		case "fmt ":
			if offset+16 > len(data) {
				return nil, 0, 0, ErrInvalidWAV
			}
			channels = int(binary.LittleEndian.Uint16(data[offset+10 : offset+12]))
			sampleRate = int(binary.LittleEndian.Uint32(data[offset+12 : offset+16]))
			fmtFound = true

		case "data":
			if !fmtFound {
				return nil, 0, 0, fmt.Errorf("WAV data chunk before fmt chunk")
			}
			dataStart := offset + 8
			dataEnd := dataStart + chunkSize
			if dataEnd > len(data) {
				dataEnd = len(data)
			}
			return bytesToInt16(data[dataStart:dataEnd]), sampleRate, channels, nil
		}

		offset += 8 + chunkSize
		if chunkSize%2 != 0 {
			offset++ // WAV chunks are word-aligned
		}
	}

	return nil, 0, 0, ErrInvalidWAV
}

// resamplePCM linearly interpolates PCM samples from fromRate to toRate.
// Good enough for voice TTS where integer or near-integer ratios are typical (e.g. 24kHz→16kHz).
func resamplePCM(pcm []int16, fromRate, toRate int) []int16 {
	if fromRate == toRate || len(pcm) == 0 {
		return pcm
	}
	outLen := int(float64(len(pcm)) * float64(toRate) / float64(fromRate))
	out := make([]int16, outLen)
	for i := range out {
		srcPos := float64(i) * float64(fromRate) / float64(toRate)
		lo := int(srcPos)
		frac := srcPos - float64(lo)
		if lo+1 < len(pcm) {
			out[i] = int16(float64(pcm[lo])*(1-frac) + float64(pcm[lo+1])*frac)
		} else {
			out[i] = pcm[lo]
		}
	}
	return out
}

// pcmToOpusFrames encodes 16 kHz mono PCM samples to a slice of Opus packets.
// Each packet covers opusFrameSamples (60 ms). The last frame is zero-padded if short.
func pcmToOpusFrames(pcm []int16) ([][]byte, error) {
	enc, err := opus.NewEncoder(OpusSampleRate, OpusChannels, opus.AppVoIP)
	if err != nil {
		return nil, fmt.Errorf("create opus encoder: %w", err)
	}
	if err := enc.SetBitrate(24000); err != nil {
		return nil, fmt.Errorf("set opus bitrate: %w", err)
	}

	buf := make([]byte, 4000)
	var frames [][]byte

	for i := 0; i < len(pcm); i += opusFrameSamples {
		frame := pcm[i:]
		if len(frame) < opusFrameSamples {
			// Zero-pad the last short frame so libopus gets a full block
			padded := make([]int16, opusFrameSamples)
			copy(padded, frame)
			frame = padded
		} else {
			frame = frame[:opusFrameSamples]
		}

		n, err := enc.Encode(frame, buf)
		if err != nil {
			return nil, fmt.Errorf("encode opus frame %d: %w", i/opusFrameSamples, err)
		}
		if n > 0 {
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			frames = append(frames, pkt)
		}
	}

	return frames, nil
}

// wavToOpusFrames converts a WAV file to Opus frames ready for the ESP32.
// It handles stereo→mono downmix and resampling to OpusSampleRate (16 kHz).
func wavToOpusFrames(wav []byte) ([][]byte, error) {
	pcm, sampleRate, channels, err := parseWAV(wav)
	if err != nil {
		return nil, fmt.Errorf("parse WAV: %w", err)
	}

	// Downmix stereo to mono by averaging channels
	if channels == 2 {
		mono := make([]int16, len(pcm)/2)
		for i := range mono {
			mono[i] = int16((int32(pcm[2*i]) + int32(pcm[2*i+1])) / 2)
		}
		pcm = mono
	}

	// Resample to device sample rate (e.g. 24kHz omlx → 16kHz device)
	if sampleRate != OpusSampleRate {
		pcm = resamplePCM(pcm, sampleRate, OpusSampleRate)
	}

	return pcmToOpusFrames(pcm)
}

// bytesToInt16 converts a little-endian byte slice to int16 samples.
func bytesToInt16(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*2 : i*2+2]))
	}
	return out
}
