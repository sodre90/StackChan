FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
RUN pip install --no-cache-dir piper-tts fastapi uvicorn

# Hungarian voice (anna, medium quality)
RUN mkdir -p /voices && cd /voices && \
    curl -fsSL -o hu_HU-anna-medium.onnx \
        "https://huggingface.co/rhasspy/piper-voices/resolve/main/hu/hu_HU/anna/medium/hu_HU-anna-medium.onnx" && \
    curl -fsSL -o hu_HU-anna-medium.onnx.json \
        "https://huggingface.co/rhasspy/piper-voices/resolve/main/hu/hu_HU/anna/medium/hu_HU-anna-medium.onnx.json"

COPY piper_server.py .
EXPOSE 14000
CMD ["python3", "piper_server.py", \
     "--port", "14000", \
     "--voice", "/voices/hu_HU-anna-medium.onnx"]
