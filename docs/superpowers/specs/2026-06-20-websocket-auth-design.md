# WebSocket Authentication — Design Spec

**Date:** 2026-06-20
**Status:** Approved (design); ready for implementation planning
**Author:** brainstorming session

## Goal

Add authentication to the StackChan server's two WebSocket endpoints so that
unauthenticated clients cannot reach them, without breaking the existing,
working setup (self-hosted robot firmware + official Android companion app).

## Background / Findings

The server exposes two WebSocket endpoints, neither currently authenticated:

1. **AI WebSocket** — `/xiaozhi/ws`, handled by `ai.Handler`
   (`server/internal/ai/protocol.go`). The robot firmware's xiaozhi protocol
   connects here for ASR → LLM → TTS. **This is the high-stakes endpoint**: it
   exposes MCP tools that control Home Assistant, place kifli.hu grocery orders,
   and read/modify the user's Google Calendar.

2. **Relay WebSocket** — `/stackChan/ws`, handled by `web_socket.Handler`
   (`server/internal/web_socket/web_socket.go`). A message relay between the
   robot (`deviceType=StackChan`) and a companion app (`deviceType=App`) for
   calls, camera streaming, and avatar control. Identity is currently just the
   `mac` + `deviceType` + `deviceId` query params; `CheckOrigin` returns `true`.

Key facts that shaped the design (verified in the firmware/app/server source):

- The xiaozhi firmware (`firmware/xiaozhi-esp32/main/protocols/websocket_protocol.cc`)
  **already** sends `Authorization: Bearer <token>` on the AI WS connect *if* a
  token is provisioned. The token comes from NVS `Settings("websocket")/"token"`.
- The firmware's OTA handler (`firmware/xiaozhi-esp32/main/ota.cc`, lines
  186–205) **persists every string key** under the OTA response's `websocket`
  object into that same NVS namespace. So adding `"token"` to the server's OTA
  response provisions the AI WS token with **no firmware code change**; the
  device picks it up on its next OTA poll (boot).
- The server's OTA response (`server/internal/cmd/cmd.go`, the `/xiaozhi/ota/`
  handler) currently **omits** the `token` field, and `ai.Handler` never checks
  any token.
- The relay's robot side (`firmware/main/hal/hal_ws_avatar.cpp`) **already**
  sends `Authorization: <token>`, where `token = secret_logic::generate_auth_token()`
  — currently the hardcoded weak default `"hi-stack-chan"` (a `__attribute__((weak))`
  function in `firmware/main/hal/utils/secret_logic/secret_logic.cpp`, meant to
  be overridden in a private build).
- The companion app the user runs is the **official, closed-source Google Play
  app** `com.m5stack.stackchan`. Its source is not in this repo (only an *iOS*
  app is, under `app/`), and it cannot be modified. It connects **only** to the
  relay as `deviceType=App` and sends no `Authorization` header.
- The `server/internal/web_socket` package builds and tests **without** the
  opus/CGO dependency that `internal/ai` pulls in. This must be preserved:
  `web_socket` must not import `internal/ai`.

## Decisions

These were settled during brainstorming:

1. **Scheme:** a single **shared bearer token** (not per-device, not mTLS).
   Single-user home LAN.
2. **Enforcement:** **hard gate** from first deploy — no soft/log-only phase, no
   enforce flag. The user will coordinate the rollout.
3. **Relay robot-side token source:** the firmware's `generate_auth_token()`
   reads the token from **NVS** (the same `Settings("websocket")/"token"` key the
   OTA flow provisions). One strong token everywhere, never compiled into the
   binary or committed to git.
4. **Relay app side (`deviceType=App`):** **left open** (unauthenticated). The
   Android app is an unmodifiable Play Store binary and cannot carry a token; a
   hard gate there would lock the user out. Documented as a known limitation.

## Architecture

One shared secret (`openssl rand -hex 32`) is the single source of truth, stored
in the server's `additional_config.yaml` (gitignored). The server hands it to the
robot via the existing OTA channel. The robot's firmware reads it from NVS and
sends it on both of its connections. Every gated WebSocket upgrade is guarded by
a constant-time comparison; failure returns 401 and refuses the upgrade.

Validation logic lives in one small, dependency-free package
(`server/internal/auth`) that both handlers call. This keeps the logic DRY,
unit-testable in isolation, and — critically — keeps `internal/web_socket` free
of the opus/CGO dependency (it must not import `internal/ai`).

### Gating matrix

| Endpoint / leg | Auth | Why |
|---|---|---|
| AI WS `/xiaozhi/ws` | Hard token gate | High-stakes MCP tools; fully controllable |
| Relay `deviceType=StackChan` | Hard token gate | Robot firmware; fully controllable |
| Relay `deviceType=App` | **Open** | Unmodifiable Play Store app |

## Components (exact files)

### Server (Go)

1. **`server/internal/auth/auth.go`** — NEW, standard library only.
   - `func SetToken(t string)` — stores the configured token (called once at
     startup). Guarded by a `sync.RWMutex` or `atomic.Value`.
   - `func Validate(authHeader string) bool` — strips an optional, case-insensitive
     `"Bearer "` prefix (single space) from `authHeader`, then compares against the
     configured token with `crypto/subtle.ConstantTimeCompare`. Returns `false` if
     the configured token is empty (fail closed) or the header is empty.
   - **`server/internal/auth/auth_test.go`** — table-driven tests (see Testing).

2. **`server/internal/ai/config.go`** — add a field to the `Config` struct:
   ```go
   // Shared bearer token required on the AI and relay (robot-side) WebSockets.
   WSAuthToken string `yaml:"ws_auth_token" json:"ws_auth_token"`
   ```
   Loaded via the existing `additional_config.yaml` overlay (no other change to
   `LoadConfig`).

3. **`server/internal/cmd/cmd.go`** — in the `Main` command `Func`, after
   `ai.Initialize(aiConfig)`:
   - If `aiConfig.WSAuthToken == ""`, log a fatal error and exit (refuse to start
     an accidentally-open server).
   - Otherwise call `auth.SetToken(aiConfig.WSAuthToken)`.
   - In the `/xiaozhi/ota/` handler, add `"token": aiConfig.WSAuthToken` to the
     `websocket` map in `otaResponse`.

4. **`server/internal/ai/protocol.go`** — in `Handler`, **before**
   `aiWSUpGrader.Upgrade(...)`:
   ```go
   if !auth.Validate(r.Request.Header.Get("Authorization")) {
       logger.Warningf(ctx, "AI WS auth rejected: mac=%s remote=%s", mac, r.RemoteAddr)
       r.Response.WriteStatus(http.StatusUnauthorized, "unauthorized")
       return
   }
   ```
   (Placed after the existing `mac` resolution so the log line carries the mac.)

5. **`server/internal/web_socket/web_socket.go`** — in `Handler`, **before**
   `wsUpGrader.Upgrade(...)`, gate **only** the robot side:
   ```go
   if deviceType == "StackChan" && !auth.Validate(r.Request.Header.Get("Authorization")) {
       logger.Warningf(ctx, "Relay WS auth rejected: mac=%s remote=%s", mac, r.RemoteAddr)
       r.Response.WriteStatus(http.StatusUnauthorized, "unauthorized")
       return
   }
   ```
   The `deviceType == "App"` path is untouched (open).

### Firmware (C++)

6. **`firmware/main/hal/utils/secret_logic/secret_logic.cpp`** — change the weak
   `generate_auth_token()` so it returns the NVS-provisioned token instead of the
   hardcoded constant:
   ```cpp
   __attribute__((weak)) std::string generate_auth_token()
   {
       Settings settings("websocket", false);
       return settings.GetString("token");
   }
   ```
   (Include the `Settings` header as the rest of the firmware does.) The AI WS
   path already reads the same key; no other firmware change is needed. If the
   token is unset in NVS this returns empty, which the hard gate correctly
   rejects — the device must be OTA-provisioned first.

### Companion app

No changes. The user is on the closed-source Android app; the relay app side is
left open.

## Data flow

1. Robot boots → OTA poll `GET /xiaozhi/ota/` → server returns `websocket.token`
   = shared token → firmware persists it to NVS `Settings("websocket")/"token"`.
2. AI WS connect: firmware reads NVS token → sends `Authorization: Bearer <token>`
   → server `auth.Validate` accepts → upgrade proceeds.
3. Relay (robot) connect: firmware `generate_auth_token()` reads the same NVS
   token → sends `Authorization: <token>` → server `auth.Validate` accepts →
   upgrade proceeds.
4. Relay (app) connect: Android app sends no token → server skips the gate for
   `deviceType=App` → upgrade proceeds (open).
5. Any gated client with a wrong/missing token → server returns 401, no upgrade.

## Error handling

- Wrong/missing token on a gated endpoint → HTTP 401 + close, **before** the
  WebSocket upgrade. Reuses the existing pre-upgrade rejection pattern in both
  handlers.
- Log rejections at WARN with the remote address and mac; **never log the token
  value**.
- Constant-time comparison (`crypto/subtle.ConstantTimeCompare`) to avoid a
  timing oracle.
- Empty `ws_auth_token` at startup → fatal exit, so the server never silently
  runs open.

## Testing

- **`server/internal/auth/auth_test.go`** — table-driven `Validate` tests:
  - valid token with `Bearer ` prefix → accept
  - valid token raw (no prefix) → accept
  - wrong token → reject
  - empty header → reject
  - configured token empty → reject regardless of header
  - lowercase `bearer ` prefix with a valid token → accept (prefix match is
    case-insensitive on the word "Bearer", exactly one trailing space)
- Existing `web_socket` race tests (`web_socket_test.go`) call the `read*`
  helpers directly, not `Handler`, so they are unaffected and must remain
  opus-free (no new imports that pull in CGO).
- Manual verification after deploy:
  - `curl` / `websocat` the AI WS and the relay robot side with no token → 401.
  - With the correct token → upgrade succeeds.
  - Reflash firmware, reboot robot → both legs reconnect (logs show no auth
    rejections).
  - Android app continues to connect (app side open).

## Rollout (hard gate, coordinated)

Expect a brief window where the robot is offline until it is reflashed and
re-OTAs. Steps:

1. Generate the token: `openssl rand -hex 32`.
2. Add `ws_auth_token: "<token>"` to `server/additional_config.yaml` (gitignored).
3. Rebuild and redeploy the server (per the GitOps flow: `build --no-cache
   stackchan` then `up -d --force-recreate stackchan`). The AI WS and relay
   robot side now require the token.
4. Reflash the robot firmware (the `secret_logic` NVS-read change). On boot it
   OTA-polls, receives the token, and reconnects to both legs.
5. The Android app keeps working unchanged (app side left open).

## Known limitations

- The relay's app side (`deviceType=App`) is unauthenticated. A LAN client that
  knows the robot's MAC can subscribe to its camera stream or initiate calls.
  This is an accepted trade-off because the official Android companion app is a
  closed-source binary that cannot carry a token.
- All WebSocket traffic is plaintext `ws://` on the LAN. The bearer token is
  therefore sniffable/spoofable by an active attacker on the same network. The
  gate blocks casual/unknown clients, not a determined LAN-level adversary.
  Stronger transport security (TLS / mTLS) was explicitly declined as out of
  scope for a single-user home LAN.

## Out of scope

- Per-device tokens / token revocation.
- TLS / mTLS / certificate provisioning.
- Any change to the companion app (iOS source or the Android binary).
- Authenticating the relay's `deviceType=App` side.
