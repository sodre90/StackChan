package ai

import (
	"context"
	"strings"
	"testing"
)

func TestFetchCryptoPrice(t *testing.T) {
	ctx := context.Background()

	result, err := fetchCryptoPrice(ctx, "BTCUSDT", "Bitcoin")
	if err != nil {
		t.Fatalf("fetchCryptoPrice(BTCUSDT): %v", err)
	}
	t.Logf("bitcoin: %s", result)

	if !strings.Contains(result, "Bitcoin") {
		t.Errorf("expected result to contain 'Bitcoin', got: %s", result)
	}
	if !strings.Contains(result, "$") {
		t.Errorf("expected result to contain '$', got: %s", result)
	}
}

func TestFetchStockPrice(t *testing.T) {
	ctx := context.Background()

	result, err := fetchStockPrice(ctx, "AAPL")
	if err != nil {
		t.Fatalf("fetchStockPrice(AAPL): %v", err)
	}
	t.Logf("AAPL: %s", result)

	if !strings.Contains(result, "AAPL") {
		t.Errorf("expected result to contain 'AAPL', got: %s", result)
	}
}

func TestHandleGetPrice_Crypto(t *testing.T) {
	m := NewMCPManager()
	ctx := context.Background()

	tests := []struct {
		input    string
		wantCoin string
	}{
		{"bitcoin", "bitcoin"},
		{"BTC", "bitcoin"},
		{"ethereum", "ethereum"},
		{"ETH", "ethereum"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result, err := m.handleGetPrice(ctx, &AIClient{Mac: "test"}, map[string]interface{}{"asset": tc.input})
			if err != nil {
				t.Fatalf("handleGetPrice(%s): %v", tc.input, err)
			}
			t.Logf("%s → %s", tc.input, result)
			if result == "" {
				t.Errorf("got empty result for %s", tc.input)
			}
		})
	}
}

func TestHandleGetPrice_Stock(t *testing.T) {
	m := NewMCPManager()
	ctx := context.Background()

	result, err := m.handleGetPrice(ctx, &AIClient{Mac: "test"}, map[string]interface{}{"asset": "TSLA"})
	if err != nil {
		t.Fatalf("handleGetPrice(TSLA): %v", err)
	}
	t.Logf("TSLA → %s", result)
	if result == "" {
		t.Error("got empty result for TSLA")
	}
}

func TestHandleGetPrice_InvalidAsset(t *testing.T) {
	m := NewMCPManager()
	ctx := context.Background()

	_, err := m.handleGetPrice(ctx, &AIClient{Mac: "test"}, map[string]interface{}{"asset": "NOTAREALTICKERXYZ123"})
	if err == nil {
		t.Error("expected error for invalid ticker, got none")
	}
	t.Logf("error (expected): %v", err)
}

func TestDDGInstantAnswer(t *testing.T) {
	ctx := context.Background()
	result := ddgInstantAnswer(ctx, "golang programming language")
	t.Logf("DDG instant answer: %q", result)
	// May be empty if DDG has no instant answer — that's fine
}

func TestWebSearch_NoKey(t *testing.T) {
	m := NewMCPManager() // no Brave key
	ctx := context.Background()

	result, err := m.handleWebSearch(ctx, &AIClient{Mac: "test"}, map[string]interface{}{"query": "golang programming language"})
	if err != nil {
		t.Fatalf("handleWebSearch: %v", err)
	}
	t.Logf("web search result: %s", result)
}

func TestWavToOpusFrames(t *testing.T) {
	// Build a minimal 24kHz mono WAV with 0.5s of silence (12000 samples)
	const srcRate = 24000
	numSamples := srcRate / 2
	pcm := make([]int16, numSamples) // silence

	// Build WAV bytes
	wav := buildWavFile(pcm, srcRate, 1, 16)

	frames, err := wavToOpusFrames(wav)
	if err != nil {
		t.Fatalf("wavToOpusFrames: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("expected at least one Opus frame")
	}

	// 0.5s at 16kHz = 8000 samples → 8000/960 ≈ 9 frames (rounded up)
	expectedFrames := (8000 + opusFrameSamples - 1) / opusFrameSamples
	t.Logf("got %d frames (expected ~%d)", len(frames), expectedFrames)
	if len(frames) < expectedFrames-1 || len(frames) > expectedFrames+1 {
		t.Errorf("unexpected frame count: got %d, want ~%d", len(frames), expectedFrames)
	}

	// Each frame must be a valid non-empty byte slice
	for i, f := range frames {
		if len(f) == 0 {
			t.Errorf("frame %d is empty", i)
		}
	}
}

func TestGetWeather(t *testing.T) {
	m := NewMCPManager()
	ctx := context.Background()

	result, err := m.handleGetWeather(ctx, &AIClient{Mac: "test"}, map[string]interface{}{"location": "Budapest"})
	if err != nil {
		t.Fatalf("handleGetWeather: %v", err)
	}
	t.Logf("weather: %s", result)
	if !strings.Contains(result, "°C") {
		t.Errorf("expected temperature in result, got: %s", result)
	}
}

func TestGetCurrentDatetime(t *testing.T) {
	m := NewMCPManager()
	ctx := context.Background()

	result, err := m.handleGetCurrentDatetime(ctx, &AIClient{Mac: "test"}, nil)
	if err != nil {
		t.Fatalf("handleGetCurrentDatetime: %v", err)
	}
	t.Logf("datetime: %s", result)
	if result == "" {
		t.Error("got empty datetime")
	}
}
