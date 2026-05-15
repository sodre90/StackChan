# Configuration Reference

All StackChan server settings live in `server/config.yaml`.
The file is hot-reloaded on server restart; no rebuild is needed.

## Secrets: `additional_config.yaml`

For API keys and other secrets, create `server/additional_config.yaml` (gitignored).
It is automatically merged on top of `config.yaml` — any key set in the additional file overrides the main config.

```yaml
# server/additional_config.yaml (not committed to git)
api_key: "your-secret-api-key"
brave_search_api_key: "your-brave-key"
```

When running via Docker Compose, both files are bind-mounted into the container.

---

## LLM (Language Model)

```yaml
llm_provider: "openai"
api_base_url: "http://host.docker.internal:8000/v1"
api_key: ""
llm_model: "qwen2.5:7b"
stream_llm: true
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `llm_provider` | string | `"openai"` | LLM provider: `"openai"` (OpenAI-compatible APIs) or `"gemini"` (Google Gemini API). |
| `api_base_url` | string | — | OpenAI-compatible base URL for the LLM. Ignored when `llm_provider` is `"gemini"`. Use `host.docker.internal` when running via Docker Compose. |
| `api_key` | string | `""` | API key. For OpenAI-compatible: leave empty for local models. For Gemini: your Google AI Studio API key (required). Best placed in `additional_config.yaml`. |
| `llm_model` | string | — | Model name. For OpenAI-compatible: as it appears in the LLM server (e.g. `qwen2.5:7b`). For Gemini: model ID (e.g. `gemini-2.0-flash`, `gemini-2.5-flash`). |
| `stream_llm` | bool | `true` | Stream LLM tokens for faster first-sentence TTS. Disable if your LLM backend does not support streaming. |
| `context_messages` | int | `10` | Number of recent conversation turns to keep in the LLM context window. Higher = better memory, higher latency. |

### Supported LLM backends

| Backend | `llm_provider` | `api_base_url` | Notes |
|---------|----------------|---------------|-------|
| Ollama | `openai` | `http://host.docker.internal:11434/v1` | Free, local, many models |
| llama.cpp | `openai` | `http://host.docker.internal:8000/v1` | Local, fast on Apple Silicon |
| LM Studio | `openai` | `http://host.docker.internal:1234/v1` | GUI model manager |
| vLLM | `openai` | `http://host.docker.internal:8000/v1` | GPU-accelerated |
| OpenAI | `openai` | `https://api.openai.com/v1` | Requires `api_key` |
| Google Gemini | `gemini` | *(ignored)* | Requires `api_key` from [Google AI Studio](https://aistudio.google.com/apikey) |
| Any OpenAI-compatible | `openai` | custom URL | Set `api_key` if needed |

### Gemini quick start

1. Get a free API key at [Google AI Studio](https://aistudio.google.com/apikey)
2. Set in `config.yaml`:
   ```yaml
   llm_provider: "gemini"
   llm_model: "gemini-2.0-flash"
   ```
3. Set in `additional_config.yaml`:
   ```yaml
   api_key: "AIzaSy..."
   ```

---

## ASR (Speech-to-Text)

```yaml
asr_base_url: "http://whisper:13000/v1"
asr_model: "whisper"
asr_language: "en"
enable_asr: true
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `asr_base_url` | string | — | OpenAI-compatible base URL for the Whisper ASR server. |
| `asr_model` | string | `"whisper"` | Model identifier sent to the ASR server. |
| `asr_language` | string | `"en"` | ISO 639-1 language code hint for Whisper. Improves accuracy and speed. Use `"auto"` for automatic detection. |
| `enable_asr` | bool | `true` | Set to `false` to disable ASR; the device must then send text messages directly. |

### Common language codes

| Language | Code |
|----------|------|
| English | `en` |
| Hungarian | `hu` |
| German | `de` |
| French | `fr` |
| Spanish | `es` |
| Japanese | `ja` |
| Chinese | `zh` |

---

## TTS (Text-to-Speech)

```yaml
tts_base_url: "http://tts:14000/v1"
tts_model: "edge-tts"
tts_voice: "en-US-AvaNeural"
tts_response_format: "opus"
enable_tts: true
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `tts_base_url` | string | — | OpenAI-compatible base URL for the TTS server. |
| `tts_model` | string | `"edge-tts"` | TTS model name sent to the server. |
| `tts_voice` | string | — | Voice name. Any edge-tts voice is accepted. |
| `tts_response_format` | string | `"opus"` | Audio format returned by the TTS server. `opus` is recommended (low latency, native ESP32 support). |
| `enable_tts` | bool | `true` | Set to `false` to disable TTS; the device will display text only. |

### Selecting a TTS voice

List all available voices and filter by language:

```bash
docker compose exec tts python3 -c \
  "import asyncio, edge_tts; voices = asyncio.run(edge_tts.list_voices()); [print(v['ShortName']) for v in voices if 'en-US' in v['ShortName']]"
```

Popular English voices:

| Voice name | Gender | Accent |
|------------|--------|--------|
| `en-US-AvaNeural` | Female | US |
| `en-US-AndrewNeural` | Male | US |
| `en-US-EmmaNeural` | Female | US |
| `en-GB-SoniaNeural` | Female | UK |
| `en-AU-NatashaNeural` | Female | AU |

---

## Personality

```yaml
system_prompt: "You are StackChan, a cute AI desktop robot..."
```

| Key | Type | Description |
|-----|------|-------------|
| `system_prompt` | string | The LLM system prompt. Controls personality, language, response length, and tone. |

Tips for the system prompt:
- Instruct the model to keep responses short (responses are spoken aloud)
- Specify the output language explicitly if it differs from `asr_language`
- Avoid instructing the model to use emojis or special characters

---

## Voice Activity Detection (VAD)

```yaml
vad_silence_timeout_ms: 800
vad_ticker_interval_ms: 100
vad_rms_threshold: 0.05
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `vad_silence_timeout_ms` | int | `800` | Milliseconds of silence after speech before the utterance is sent for transcription. Lower = more responsive, higher = fewer cut-off sentences. |
| `vad_ticker_interval_ms` | int | `100` | How often (ms) the server scans incoming audio for end-of-speech. Lower = faster detection, slightly more CPU. |
| `vad_rms_threshold` | float | `0.05` | RMS energy threshold (0.0–1.0) used to distinguish speech from background noise. Raise this if background noise causes false triggers. |

### Tuning VAD

**Response feels slow after you stop talking** → lower `vad_silence_timeout_ms` (try `600`)

**Utterances get cut off** → raise `vad_silence_timeout_ms` (try `1000`)

**Background noise triggers false speech detection** → raise `vad_rms_threshold` (try `0.07`–`0.10`)

**Quiet voice not detected** → lower `vad_rms_threshold` (try `0.025`)

**Whisper logs `VAD filter removed ~Xs of audio`** → raise `vad_rms_threshold` until false-positive frames stop

---

## MCP Tools

```yaml
enable_mcp_tools: true
brave_search_api_key: ""
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enable_mcp_tools` | bool | `true` | Expose built-in tools to the LLM so it can control the robot and query external services. |
| `brave_search_api_key` | string | `""` | Optional [Brave Search API key](https://search.brave.com/resources/api). When set, `web_search` uses the full Brave index for much richer results. Without it, web_search falls back to DuckDuckGo instant answers. |

When enabled, the LLM can call:

| Tool | What it does |
|------|-------------|
| `robot.set_head_angles` | Move head: `yaw` (−90° to 90°), `pitch` (−45° to 45°) |
| `robot.get_head_angles` | Read current head position |
| `robot.set_led_color` | Set RGB LEDs: `red`, `green`, `blue` (0–255) |
| `robot.play_expression` | Play an emotion: `happy`, `sad`, `angry`, `surprised`, `sleepy`, `thinking`, `love`, `dancing` |
| `robot.play_dance` | Play a dance: `default`, `wave`, `spin`, `jump` |
| `robot.create_reminder` | Schedule a spoken reminder after `delay_seconds` seconds |
| `robot.get_reminders` | List active reminders |
| `robot.stop_reminder` | Cancel a reminder by ID |
| `get_weather` | Current weather + 3-day forecast for a location |
| `get_current_datetime` | Current date, time, and weekday |
| `get_price` | Current price + 24 h change for crypto or stocks |
| `web_search` | Search the web for recent information |

---

## Network

```yaml
ws_port: 0
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `ws_port` | int | `0` | Separate port for the WebSocket endpoint. `0` means use the main server port (12800). |

The main server always listens on port **12800**. Change the host-side binding in `docker-compose.yml` if you need a different external port.

---

## Complete examples

### Local LLM (OpenAI-compatible)

```yaml
llm_provider: "openai"
api_base_url: "http://host.docker.internal:8000/v1"
asr_base_url: "http://whisper:13000/v1"
tts_base_url: "http://tts:14000/v1"
api_key: ""

asr_model: "whisper"
llm_model: "qwen2.5:7b"
tts_model: "edge-tts"
tts_voice: "en-US-AvaNeural"
tts_response_format: "opus"

system_prompt: "You are StackChan, a cute AI desktop robot. Be friendly, helpful, and concise. Keep responses under 30 words. Speak in English."

enable_asr: true
enable_tts: true
context_messages: 10
stream_llm: true

asr_language: "en"
vad_silence_timeout_ms: 800
vad_ticker_interval_ms: 100
vad_rms_threshold: 0.05

ws_port: 0
enable_mcp_tools: true
```

### Google Gemini

```yaml
llm_provider: "gemini"
llm_model: "gemini-2.0-flash"
# api_key goes in additional_config.yaml

asr_base_url: "http://host.docker.internal:13000/v1"
tts_base_url: "http://tts:14000/v1"

asr_model: "whisper"
tts_model: "piper"
tts_voice: "en-US-AvaNeural"
tts_response_format: "opus"

system_prompt: "You are StackChan, a cute AI desktop robot. Be friendly, helpful, and concise. Keep responses under 30 words. Speak in English."

enable_asr: true
enable_tts: true
context_messages: 10
stream_llm: true

asr_language: "en"
vad_silence_timeout_ms: 800
vad_ticker_interval_ms: 100
vad_rms_threshold: 0.05

ws_port: 0
enable_mcp_tools: true
```
