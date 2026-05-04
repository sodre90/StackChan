#!/usr/bin/env python3
"""
Local TTS server using Piper.
Exposes an OpenAI-compatible /v1/audio/speech endpoint.
Outputs Opus audio at 16kHz mono in OGG container (matches xiaozhi-esp32 expectations).

Usage:
    python3 piper_server.py [--port 14000] --voice /voices/hu_HU-anna-medium.onnx
"""

import argparse
import io
import logging
import subprocess
import wave
from typing import Optional

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import Response
from piper import PiperVoice

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("piper-server")

app = FastAPI(title="Local Piper TTS Server")

voice: Optional[PiperVoice] = None
voice_name: str = ""


def synthesize_wav(text: str) -> bytes:
    buf = io.BytesIO()
    with wave.open(buf, "wb") as wav_file:
        voice.synthesize_wav(text, wav_file)
    return buf.getvalue()


def wav_to_opus(wav_bytes: bytes) -> bytes:
    proc = subprocess.run(
        [
            "ffmpeg", "-y", "-loglevel", "error",
            "-f", "wav", "-i", "pipe:0",
            "-c:a", "libopus",
            "-b:a", "24000",
            "-ar", "16000",
            "-ac", "1",
            "-frame_duration", "60",
            "-application", "voip",
            "-f", "ogg", "pipe:1",
        ],
        input=wav_bytes, capture_output=True, check=False,
    )
    if proc.returncode != 0:
        logger.error(f"ffmpeg error: {proc.stderr.decode(errors='replace')}")
        raise HTTPException(status_code=500, detail="ffmpeg conversion failed")
    return proc.stdout


@app.post("/v1/audio/speech")
async def speech(request: Request):
    if voice is None:
        raise HTTPException(status_code=503, detail="Voice not loaded")

    body = await request.json()
    text = body.get("input", "")
    response_format = body.get("response_format", "opus")
    if not text:
        raise HTTPException(status_code=400, detail="No input text provided")

    logger.info(f"TTS: voice={voice_name}, format={response_format}, text={text[:60]}")

    try:
        wav = synthesize_wav(text)
    except Exception as e:
        logger.exception("Piper synthesis failed")
        raise HTTPException(status_code=500, detail=str(e))

    fmt = response_format.lower()
    if fmt == "opus":
        audio = wav_to_opus(wav)
        media = "audio/ogg"
    elif fmt == "wav":
        audio = wav
        media = "audio/wav"
    else:
        raise HTTPException(status_code=400, detail=f"Unsupported format: {fmt}")

    logger.info(f"TTS complete: {len(wav)} bytes WAV -> {len(audio)} bytes ({fmt})")
    return Response(content=audio, media_type=media)


@app.get("/v1/models")
async def list_models():
    return {"object": "list", "data": [{"id": "piper", "object": "model"}]}


@app.get("/health")
async def health():
    return {"status": "ok", "voice": voice_name}


def main():
    global voice, voice_name
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=14000)
    parser.add_argument("--voice", required=True,
                        help="Path to Piper voice .onnx file")
    args = parser.parse_args()

    voice_name = args.voice.split("/")[-1].replace(".onnx", "")
    logger.info(f"Loading Piper voice: {args.voice}")
    voice = PiperVoice.load(args.voice)
    logger.info(f"Voice loaded: {voice_name}")

    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=args.port, log_level="info")


if __name__ == "__main__":
    main()
