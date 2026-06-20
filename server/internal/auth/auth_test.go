/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package auth

import "testing"

func TestValidate(t *testing.T) {
	const tok = "s3cr3t-abc123"
	tests := []struct {
		name   string
		set    string // configured token
		header string // incoming Authorization header
		want   bool
	}{
		{"raw token", tok, tok, true},
		{"bearer prefix", tok, "Bearer " + tok, true},
		{"lowercase bearer prefix", tok, "bearer " + tok, true},
		{"wrong token", tok, "nope", false},
		{"wrong token with bearer", tok, "Bearer nope", false},
		{"empty header", tok, "", false},
		{"configured token empty rejects raw", "", tok, false},
		{"configured token empty rejects empty", "", "", false},
		{"bearer prefix only, no token", tok, "Bearer ", false},
		{"bearer without trailing space not stripped", tok, "Bearer" + tok, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetToken(tt.set)
			if got := Validate(tt.header); got != tt.want {
				t.Fatalf("Validate(%q) with configured token %q = %v, want %v",
					tt.header, tt.set, got, tt.want)
			}
		})
	}
}
