# StackChan Server

**StackChan Server** is the Server of the open-source StackChan project. It handles core functionalities such
as device interactions, post management, and comment systems, providing stable and efficient API support.

---

## Features

- App and StackChan communication and interaction
- Device post creation and management (text and image support, similar to a social feed)
- Comment CRUD (Create, Read, Update, Delete) operations
- Dance control and data management
- Persistent storage using a relational database
- **AI voice pipeline** — speech-to-text → LLM → text-to-speech via WebSocket
- **Multiple LLM providers** — OpenAI-compatible (Ollama, llama.cpp, vLLM, OpenAI) or Google Gemini API
- **MCP tools** — weather, web search, crypto/stock prices, robot control (head, LED, expressions, dance, reminders)

### Local AI setup

For the full local AI setup (Whisper ASR + local LLM + TTS), see **[LOCAL_AI_SETUP.md](LOCAL_AI_SETUP.md)**.

For all configuration options, see **[CONFIGURATION.md](CONFIGURATION.md)**.

---

## Getting Started

### Prerequisites

- **Go**: The project is developed in Go. Install **Go 1.24+** from
  the [official download page](https://golang.google.cn/dl/).

Verify installation:

```bash
go version
# Expected output: "go version go1.24.x ..." (or similar)
```

### Clone the Repository

```bash
git clone https://github.com/sodre90/StackChan
cd StackChan/server

# Download dependencies
go mod download

# build
go build -o StackChan main.go

# Start running
StackChan    # Linux/macOS
StackChan.exe  # Windows
```

### Configuration

Settings live in `config.yaml`. For secrets (API keys), create an `additional_config.yaml` (gitignored) — it is automatically merged on top:

```yaml
# additional_config.yaml
api_key: "your-api-key-here"
```
