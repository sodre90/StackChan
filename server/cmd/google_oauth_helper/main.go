// google_oauth_helper runs a one-time loopback OAuth2 flow against Google
// and prints the resulting refresh token so it can be pasted into
// server/additional_config.yaml as google_oauth_refresh_token.
//
// Usage:
//   cd server
//   go run ./cmd/google_oauth_helper \
//       -client-id YOUR_DESKTOP_CLIENT_ID \
//       -client-secret YOUR_DESKTOP_CLIENT_SECRET
//
// You must use a Desktop-app type OAuth client. Calendar.readonly scope is requested.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	authURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenURL = "https://oauth2.googleapis.com/token"
	scope    = "https://www.googleapis.com/auth/calendar.readonly"
)

func main() {
	clientID := flag.String("client-id", "", "Google OAuth client ID (Desktop app type)")
	clientSecret := flag.String("client-secret", "", "Google OAuth client secret")
	flag.Parse()

	if *clientID == "" || *clientSecret == "" {
		fmt.Fprintln(os.Stderr, "client-id and client-secret are required")
		flag.Usage()
		os.Exit(2)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		die("listen: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	state := randomState()

	q := url.Values{}
	q.Set("client_id", *clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", scope)
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("state", state)

	consentURL := authURL + "?" + q.Encode()

	fmt.Println()
	fmt.Println("Open this URL in your browser to authorize StackChan to read your calendar:")
	fmt.Println()
	fmt.Println("  " + consentURL)
	fmt.Println()
	fmt.Println("Waiting for redirect to " + redirectURI + " ...")

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("state"); got != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			done <- result{err: fmt.Errorf("state mismatch")}
			return
		}
		if errCode := r.URL.Query().Get("error"); errCode != "" {
			http.Error(w, "oauth error: "+errCode, http.StatusBadRequest)
			done <- result{err: fmt.Errorf("oauth error: %s", errCode)}
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			done <- result{err: fmt.Errorf("missing code")}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintln(w, "<html><body><h2>StackChan authorized.</h2><p>You can close this tab and return to the terminal.</p></body></html>")
		done <- result{code: code}
	})

	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()

	var res result
	select {
	case res = <-done:
	case <-time.After(5 * time.Minute):
		die("timed out waiting for browser redirect")
	}
	_ = server.Shutdown(context.Background())

	if res.err != nil {
		die("%v", res.err)
	}

	refresh, err := exchangeCode(res.code, *clientID, *clientSecret, redirectURI)
	if err != nil {
		die("exchange code: %v", err)
	}

	fmt.Println()
	fmt.Println("Success. Add the following lines to server/additional_config.yaml:")
	fmt.Println()
	fmt.Printf("google_oauth_client_id: %q\n", *clientID)
	fmt.Printf("google_oauth_client_secret: %q\n", *clientSecret)
	fmt.Printf("google_oauth_refresh_token: %q\n", refresh)
	fmt.Println()
}

func exchangeCode(code, clientID, clientSecret, redirectURI string) (string, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")

	resp, err := http.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("token endpoint %d: %s", resp.StatusCode, string(body))
	}
	var tok struct {
		RefreshToken string `json:"refresh_token"`
		AccessToken  string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", err
	}
	if tok.RefreshToken == "" {
		return "", fmt.Errorf("no refresh_token in response (did you previously authorize without prompt=consent?): %s", string(body))
	}
	return tok.RefreshToken, nil
}

func randomState() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		die("random: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
