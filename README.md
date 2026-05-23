# StackChan Open Source

<img src="https://vm.pledgebox.com/uploads/20251229/5sSmfjGLj9YYIzfHOSXQm4K00adu4Gv1.png" width="60%">

**Pre-order your StackChan**: https://m5stack.com/stackchan

---

## About this fork

This is a fork of [m5stack/StackChan](https://github.com/m5stack/StackChan) that adds a
**fully self-hosted AI backend**. Instead of relying on a cloud assistant, you run the entire
voice pipeline — speech-to-text, the language model, and text-to-speech — on your own machine,
and the device talks to it over WebSocket. Everything below the product description is new in
this fork.

### What this fork adds

- **Self-hosted voice pipeline** — `ASR → LLM → TTS` over WebSocket, all running locally. The
  device's OTA endpoint points at your own server.
- **Pluggable LLM providers** — OpenAI-compatible (Ollama, llama.cpp, vLLM, OpenAI) **or** the
  Google Gemini API, selectable via `llm_provider`.
- **Gemini priority fallback** — on a rate-limit (HTTP 429) or unavailability (5xx), the request
  automatically retries the next model in a configured chain (`llm_fallback_models`), so the
  assistant keeps responding when one model's quota is exhausted.
- **Local ASR** — Whisper via `faster-whisper`, or `mlx-whisper` for Apple-Silicon GPU; a
  `wav2vec2` server is also included as an alternative.
- **Local TTS** — `edge-tts` (Microsoft neural voices) by default, with a Piper alternative.
  Output is 16 kHz mono Opus, matched to the firmware's audio pipeline.
- **MCP tools** — web search, weather, crypto/stock prices, and robot control: head servos, RGB
  LEDs, facial expressions, dances, and reminders.
- **Home Assistant integration** — turn devices on/off, run scripts, and query entity state.
- **Google Calendar integration** (OAuth2) — list upcoming events and get proactive spoken
  reminders at configurable milestones before an event.
- **Containerized deploy** — Docker Compose **and** Podman Compose stacks (`stackchan` + `whisper`
  + `tts`).
- **Firmware tweaks** — automatic reconnect with exponential backoff after a server restart.
- **Text test endpoints** — trigger a reply over HTTP without speaking: `/xiaozhi/test/chat`,
  `/xiaozhi/test/speak`, and `/xiaozhi/test/devices`.

### Architecture

```
  ┌──────────────┐   WebSocket    ┌────────────────────────────────────────┐
  │  StackChan   │ ◀────────────▶ │  Go server (:12800)                    │
  │  (CoreS3)    │   Opus audio   │  OTA · /xiaozhi/ws · AI pipeline · MCP │
  └──────────────┘                └───────┬───────────────┬────────────────┘
                                          │               │
                              ┌───────────▼──┐   ┌────────▼─────────┐   ┌──────────────┐
                              │ Whisper ASR  │   │  LLM             │   │  edge-tts    │
                              │  (:13000)    │   │  Gemini / local  │   │  (:14000)    │
                              └──────────────┘   └──────────────────┘   └──────────────┘
```

### Get started

| Guide | What it covers |
|-------|----------------|
| [QUICKSTART.md](QUICKSTART.md) | Robot talking in ~15 min (Docker + Ollama) |
| [SERVER_SETUP.md](SERVER_SETUP.md) | Full server setup with Podman |
| [DEPLOYMENT.md](DEPLOYMENT.md) | End-to-end deployment (server + firmware + OTA) |
| [server/DOCKER_SETUP.md](server/DOCKER_SETUP.md) | Docker Compose deployment details |
| [server/LOCAL_AI_SETUP.md](server/LOCAL_AI_SETUP.md) | Running the services without containers |
| [server/CONFIGURATION.md](server/CONFIGURATION.md) | Every `config.yaml` option |
| [server/internal/ai/README.md](server/internal/ai/README.md) | AI protocol handler internals |

Secrets (API keys, OAuth tokens) go in `server/additional_config.yaml` (gitignored), which is
merged on top of `config.yaml` at startup.

### Language

The stack ships defaulting to **English**, with a ready-made **Hungarian** variant. Pick one
by the compose file you run:

| | English (default) | Hungarian |
|---|---|---|
| Compose | `podman-compose.yml` / `docker-compose.yml` | `podman-compose.hu.yml` / `docker-compose.hu.yml` |
| Config | `config.yaml` | `config.hu.yaml` |
| Whisper model | `large-v3-turbo` | `sarpba/whisper-base-hungarian_v1` |

They use the same ports — run one at a time. Secrets in `additional_config.yaml` apply to both.

For any other language, edit the language keys in `config.yaml`:

- `asr_language` — Whisper transcription language. ISO 639-1 code (e.g. `"en"`, `"hu"`) or
  `"auto"` to auto-detect. (Match it with a Whisper model that supports the language — see the
  whisper Dockerfiles.)
- `tts_voice` — edge-tts voice; the voice selects the spoken language (e.g. `en-US-AvaNeural`,
  `hu-HU-NoemiNeural`, or a multilingual voice like `en-US-AvaMultilingualNeural` that
  auto-switches by text). See the [edge-tts voice list](https://github.com/rany2/edge-tts).
- `system_prompt` — instruct the model which language to reply in.

(Proactive Google Calendar reminders are currently phrased in Hungarian in code, independently
of these settings.)

---

> The sections below describe the original StackChan product and hardware.



> The software development is still in progress. Final features and documentation may change. Thank you for your understanding. 

<img src="https://cdn.shopify.com/s/files/1/0056/7689/2250/files/5a589623895f65487717894d9240f6b8.png" width="60%">

**StackChan is a super kawaii AI desktop robot co-created by M5Stack and the user community.** It uses the M5Stack **flagship IoT development kit [CoreS3](https://docs.m5stack.com/en/core/CoreS3)** as its main controller, powered by an ESP32-S3 SoC featuring a 240 MHz dual-core processor, with 16MB Flash and 8MB PSRAM onboard, and supporting Wi-Fi and BLE. The main unit also integrates a 2.0-inch capacitive touch display with a high-strength glass cover, a 0.3 MP camera, a proximity sensor, a 9-axis IMU (accelerometer + gyroscope + magnetometer), a microSD card slot, a 1W speaker, dual microphones, and power/reset buttons. 

The **robot body**, connected to the main unit, includes a USB-C interface for power and data, a 700 mAh battery, two feedback servos (360-degree continuous rotation on the horizontal axis and 90-degree movement on the vertical axis), two rows totaling 12 RGB LEDs, infrared transmitter and receiver, a three-zone touch panel, and a full-featured NFC module. 

The **factory firmware** comes with rich features, including vivid and cute facial expressions and motions, the XiaoZhi AI agent, as well as support for iOS app video calls, remote avatars, and discovering other nearby StackChan devices. The product also supports programming via Arduino and UiFlow2, making it easy to implement a wide range of custom functionalities. 

> Do not forcibly rotate any movable parts connected to the motors by hand when you are unsure whether the motors are powered and under control, as this may cause hardware damage. 

- **StackChan World iOS app**: https://apps.apple.com/app/stackchan-world/id6756086326

- **StackChan World website**: https://stackchan.world/home
