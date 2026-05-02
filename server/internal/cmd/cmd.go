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
