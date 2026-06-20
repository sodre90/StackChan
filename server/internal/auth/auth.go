/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

// Package auth provides shared bearer-token validation for the server's
// WebSocket endpoints. It holds a single configured token (set once at
// startup) and validates incoming Authorization headers against it in
// constant time.
//
// It deliberately depends only on the standard library so that callers —
// notably internal/web_socket — can gate connections without pulling in the
// opus/CGO dependency chain of internal/ai.
package auth

import (
	"crypto/subtle"
	"strings"
	"sync"
)

var (
	mu    sync.RWMutex
	token string
)

// SetToken stores the shared bearer token required by gated endpoints. It is
// expected to be called once during startup, before any handler runs.
func SetToken(t string) {
	mu.Lock()
	token = t
	mu.Unlock()
}

// Validate reports whether authHeader carries the configured token. It accepts
// the token either raw (as the relay robot side sends it) or behind a single
// "Bearer " prefix (as the xiaozhi AI WS sends it); the prefix word is matched
// case-insensitively. It returns false if no token is configured or the header
// is empty. The token comparison is constant-time.
func Validate(authHeader string) bool {
	mu.RLock()
	want := token
	mu.RUnlock()

	if want == "" {
		return false
	}

	got := authHeader
	if len(got) >= 7 && strings.EqualFold(got[:7], "Bearer ") {
		got = got[7:]
	}

	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
