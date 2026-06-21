/*
SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
SPDX-License-Identifier: MIT
*/

package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"stackChan/internal/ai"
	"stackChan/internal/auth"
	"stackChan/internal/controller/dance"
	"stackChan/internal/controller/device"
	"stackChan/internal/controller/file"
	"stackChan/internal/controller/friend"
	"stackChan/internal/controller/post"
	"stackChan/internal/web_socket"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/os/gcmd"
	"github.com/gogf/gf/v2/os/gfile"
	"github.com/gogf/gf/v2/os/gtimer"
)

var (
	Main = gcmd.Command{
		Name:  "main",
		Usage: "main",
		Brief: "start http server",
		Func: func(ctx context.Context, parser *gcmd.Parser) (err error) {
			PrintIPAddr()

			//Start a scheduled task to send ping messages
			gtimer.SetInterval(ctx, time.Second*5, func(ctx context.Context) {
				web_socket.StartPingTime(ctx)
			})
			//Start a timer to clean up long-lived connections that have been inactive for a long time on the app.
			gtimer.SetInterval(ctx, time.Second*15, func(ctx context.Context) {
				web_socket.CheckExpiredLinks(ctx)
			})

			s := g.Server()
			s.SetPort(12800)
			s.BindHandler("/stackChan/ws", web_socket.Handler)

			///Configuration file access
			s.Group("/file", func(group *ghttp.RouterGroup) {
				group.GET("/*filepath", func(r *ghttp.Request) {
					relativePath := r.Get("filepath").String()
					if relativePath == "" {
						r.Response.WriteHeader(http.StatusNotFound)
						r.Response.Write("File not found")
						return
					}
					filePath := filepath.Join("file", relativePath)
					if !gfile.Exists(filePath) {
						r.Response.WriteHeader(http.StatusNotFound)
						r.Response.Write("File not found")
						return
					}
					r.Response.ServeFile(filePath)
				})
			})

			s.Group("/stackChan", func(group *ghttp.RouterGroup) {
				group.Middleware(ghttp.MiddlewareHandlerResponse)
				group.Bind(device.NewV1(), friend.NewV1(), dance.NewV1(), file.NewV1(), post.NewV1())
			})

			// AI protocol handler for XiaoZhi voice interaction
			aiConfig, err := ai.LoadConfig("")
			if err != nil {
				fmt.Printf("Warning: Could not load AI config, using defaults: %v\n", err)
				aiConfig = ai.DefaultConfig()
			}
			ai.Initialize(aiConfig)

			// WebSocket authentication: require a shared bearer token on the AI WS
			// and the relay's robot side. Fail closed — refuse to start with an
			// empty token rather than silently exposing unauthenticated endpoints.
			if aiConfig.WSAuthToken == "" {
				return fmt.Errorf("ws_auth_token is not set in additional_config.yaml; " +
					"refusing to start with unauthenticated WebSockets")
			}
			auth.SetToken(aiConfig.WSAuthToken)

			s.BindHandler("/xiaozhi/ws", ai.Handler)
			port := aiConfig.WSPort
			if port == 0 {
				port = 12800
			}

			// OTA endpoint - returns WebSocket config to ESP32 devices
			// The ESP32 firmware calls this URL to get the WebSocket server address
			s.BindHandler("/xiaozhi/ota/", func(r *ghttp.Request) {
				// Parse device info from headers
				deviceID := r.Header.Get("Device-Id")
				clientID := r.Header.Get("Client-Id")
				activationVersion := r.Header.Get("Activation-Version")

				// Build the OTA response JSON
				// The ESP32 expects: { "firmware": {...}, "websocket": {...}, "server_time": {...} }
				// Derive host from the inbound request so URLs point back at whatever
				// address the device used to reach us — no hardcoded IP needed.
				host, _, splitErr := net.SplitHostPort(r.Host)
				if splitErr != nil {
					host = r.Host
				}
				otaUrl := fmt.Sprintf("http://%s:12800/xiaozhi/ota/", host)
				wsUrl := fmt.Sprintf("ws://%s:12800/xiaozhi/ws", host)

				otaResponse := map[string]interface{}{
					"firmware": map[string]interface{}{
						"version": "1.0.0",
						"url":     fmt.Sprintf("http://%s/xiaozhi/firmware.bin", r.Host),
					},
					"wifi": map[string]interface{}{
						"ota_url": otaUrl,
					},
					"websocket": map[string]interface{}{
						"url":     wsUrl,
						"version": 1,
						// Shared bearer token the device echoes back on connect. The
						// firmware persists every websocket string key to NVS, so this
						// provisions both the AI WS and the relay robot side.
						"token": aiConfig.WSAuthToken,
					},
					"server_time": map[string]interface{}{
						"timestamp":        time.Now().Unix(),
						"timezone_offset":  0,
					},
				}

				// Log the OTA request for debugging
				fmt.Printf("[OTA] Request from device_id=%s, client_id=%s, activation=%s\n",
					deviceID, clientID, activationVersion)

				r.Response.WriteJson(otaResponse)
			})

			// Test endpoint: push spoken text to connected device(s) via the same
			// path as calendar announcements. Lets you verify idle playback / TTS
			// voice without waiting for a real event.
			//   GET/POST /xiaozhi/test/speak?text=...&mac=...&voice=...
			// text required; mac optional (all devices if empty); voice optional
			// (overrides configured TTS voice, e.g. hu-HU-NoemiNeural).
			s.BindHandler("/xiaozhi/test/speak", func(r *ghttp.Request) {
				text := r.Get("text").String()
				if text == "" {
					r.Response.WriteStatus(http.StatusBadRequest)
					r.Response.WriteJson(g.Map{"error": "text parameter is required"})
					return
				}
				mac := r.Get("mac").String()
				voice := r.Get("voice").String()
				count := ai.SpeakToDeviceWithVoice(r.Context(), mac, text, voice)
				r.Response.WriteJson(g.Map{
					"spoken_on_devices": count,
					"text":              text,
					"voice":             voice,
					"mac":               mac,
				})
			})

			// Test endpoint: inject text as if the user had spoken it and run the
			// FULL LLM -> streaming multi-sentence TTS pipeline to the device(s),
			// bypassing the mic/Whisper. Unlike /test/speak (single TTS call), this
			// exercises the per-sentence pipeline, so use it to test voice stutter
			// from the keyboard.
			//   GET/POST /xiaozhi/test/chat?text=...&mac=...
			// text required; mac optional (all connected devices if empty).
			s.BindHandler("/xiaozhi/test/chat", func(r *ghttp.Request) {
				text := r.Get("text").String()
				if text == "" {
					r.Response.WriteStatus(http.StatusBadRequest)
					r.Response.WriteJson(g.Map{"error": "text parameter is required"})
					return
				}
				mac := r.Get("mac").String()
				count := ai.ChatToDevice(mac, text)
				r.Response.WriteJson(g.Map{
					"sent_to_devices": count,
					"text":            text,
					"mac":             mac,
				})
			})

			// Test endpoint: run the full LLM -> tool-calling loop for one prompt
			// and RETURN the answer as JSON. Unlike /test/chat, this speaks to NO
			// device and runs NO TTS — the robot stays silent and you get the
			// model's text reply (with any tool calls already resolved) straight
			// back in the HTTP response. No device needs to be connected.
			//   GET/POST /xiaozhi/test/ask?text=...&mac=...&reasoning=true|false&temperature=0.7
			// text required; mac optional (only tags the throwaway session/tool ctx);
			// reasoning optional (omit = configured default) toggles Qwen3 thinking
			// for this call only; temperature optional (omit = default 0.7) sets the
			// LLM sampling temperature for this call only.
			s.BindHandler("/xiaozhi/test/ask", func(r *ghttp.Request) {
				text := r.Get("text").String()
				if text == "" {
					r.Response.WriteStatus(http.StatusBadRequest)
					r.Response.WriteJson(g.Map{"error": "text parameter is required"})
					return
				}
				mac := r.Get("mac").String()
				// reasoning override: absent -> nil (use config default); present ->
				// parse as bool (true/1/yes/on => on, false/0/no/off => off).
				var reasoning *bool
				if raw := r.Get("reasoning").String(); raw != "" {
					b := r.Get("reasoning").Bool()
					reasoning = &b
				}
				// temperature override: absent -> nil (use default 0.7); present ->
				// parse as float and send verbatim.
				var temperature *float64
				if raw := r.Get("temperature").String(); raw != "" {
					f := r.Get("temperature").Float64()
					temperature = &f
				}
				answer := ai.AskLLM(r.Context(), mac, text, reasoning, temperature)
				r.Response.WriteJson(g.Map{
					"text":        text,
					"answer":      answer,
					"mac":         mac,
					"reasoning":   reasoning,
					"temperature": temperature,
				})
			})

			// Test endpoint: list currently connected AI device MACs.
			s.BindHandler("/xiaozhi/test/devices", func(r *ghttp.Request) {
				macs := ai.GetActiveClients()
				r.Response.WriteJson(g.Map{"count": len(macs), "devices": macs})
			})

			// Serve firmware binary at /xiaozhi/firmware.bin (placeholder)
			s.Group("/xiaozhi", func(group *ghttp.RouterGroup) {
				group.GET("/firmware.bin", func(r *ghttp.Request) {
					r.Response.WriteHeader(http.StatusNotFound)
					r.Response.Write("Firmware not found. Please build and flash firmware separately.")
				})
			})

			fmt.Printf("AI protocol handler started at /xiaozhi/ws (port %d)\n", port)
			fmt.Printf("OTA endpoint available at /xiaozhi/ota/\n")
			fmt.Printf("AI Backend: %s (LLM: %s, ASR: %s, TTS: %s/%s)\n",
				aiConfig.APIBaseURL, aiConfig.LLMModel, aiConfig.ASRModel,
				aiConfig.TTSModel, aiConfig.TTSVoice)
			if aiConfig.StreamLLM {
				fmt.Println("LLM streaming: enabled")
			}
			if aiConfig.ContextMessages > 0 {
				fmt.Printf("Conversation context: %d message pairs\n", aiConfig.ContextMessages)
			}

			s.Run()
			return nil
		},
	}
)

func PrintIPAddr() {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		fmt.Println("Local IP addresses detected on this machine:")
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				fmt.Println("  -", ipnet.IP.String())
			}
		}
	} else {
		fmt.Println("Could not detect local IP addresses:", err)
	}
	fmt.Println("Please update the StackChan and iOS client access addresses to use one of the above local IPs as needed.")

}
