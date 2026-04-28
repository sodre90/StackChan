#!/usr/bin/env python3
"""
Local TTS server using edge-tts + ffmpeg.
Exposes an OpenAI-compatible /v1/audio/speech endpoint.
Outputs Opus audio at 16kHz mono (compatible with StackChan/xiaozhi-esp32).

Usage:
    python3 tts_server.py [--port 14000] [--voice en-US-AvaNeural]
"""

import argparse
import asyncio
import io
import logging
import subprocess
import tempfile
import os

import edge_tts
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import Response
import uvicorn

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("tts-server")

app = FastAPI(title="Local TTS Server (edge-tts)")

DEFAULT_VOICE = "hu-HU-NoemiNeural"
default_voice = DEFAULT_VOICE


@app.post("/v1/audio/speech")
async def tts(request: Request):
    body = await request.json()
    text = body.get("input", "")
    voice = body.get("voice", default_voice)
    response_format = body.get("response_format", "opus")

    if not text:
        raise HTTPException(status_code=400, detail="No input text provided")

    # Map OpenAI voice names to edge-tts voices
    voice_map = {
        "alloy":   "hu-HU-NoemiNeural",
        "echo":    "hu-HU-TamasNeural",
        "fable":   "en-GB-SoniaNeural",
        "onyx":    "en-US-OnyxNeural",
        "nova":    "en-US-NovaNeural",
        "shimmer": "en-US-ShimmerNeural",
    }
    edge_voice = voice_map.get(voice, voice if voice else default_voice)

    logger.info(f"TTS: voice={edge_voice}, format={response_format}, text={text[:60]}")

    # Generate MP3 with edge-tts
    communicate = edge_tts.Communicate(text, edge_voice)
    mp3_chunks = []
    async for chunk in communicate.stream():
        if chunk["type"] == "audio":
            mp3_chunks.append(chunk["data"])

    if not mp3_chunks:
        raise HTTPException(status_code=500, detail="edge-tts returned no audio")

    mp3_data = b"".join(mp3_chunks)

    # Convert to Opus using ffmpeg (16kHz mono, compatible with xiaozhi-esp32)
    with tempfile.NamedTemporaryFile(suffix=".mp3", delete=False) as tmp_in:
        tmp_in.write(mp3_data)
        tmp_in_path = tmp_in.name

    tmp_out_path = tmp_in_path.replace(".mp3", ".ogg")
    try:
        result = subprocess.run(
            [
                "ffmpeg", "-y",
                "-i", tmp_in_path,
                "-c:a", "libopus",
                "-b:a", "24000",
                "-ar", "16000",
                "-ac", "1",
                "-frame_duration", "60",
                "-application", "voip",
                "-f", "ogg",
                tmp_out_path,
            ],
            capture_output=True,
            timeout=30,
        )
        if result.returncode != 0:
            logger.error(f"ffmpeg error: {result.stderr.decode()}")
            raise HTTPException(status_code=500, detail="ffmpeg conversion failed")

        with open(tmp_out_path, "rb") as f:
            opus_data = f.read()
    finally:
        os.unlink(tmp_in_path)
        if os.path.exists(tmp_out_path):
            os.unlink(tmp_out_path)

    logger.info(f"TTS complete: {len(mp3_data)} bytes MP3 -> {len(opus_data)} bytes Opus")
    return Response(content=opus_data, media_type="audio/ogg")


@app.get("/v1/models")
async def list_models():
    return {"object": "list", "data": [{"id": "tts-local", "object": "model"}]}


def main():
    global default_voice
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=14000)
    parser.add_argument("--voice", default=DEFAULT_VOICE, help="Default edge-tts voice")
    args = parser.parse_args()
    default_voice = args.voice
    logger.info(f"Starting TTS server on port {args.port} with voice {default_voice}")
    uvicorn.run(app, host="0.0.0.0", port=args.port, log_level="info")


if __name__ == "__main__":
    main()
