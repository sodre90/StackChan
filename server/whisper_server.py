#!/usr/bin/env python3
"""
Local Whisper ASR server using faster-whisper.
Exposes an OpenAI-compatible /v1/audio/transcriptions endpoint.

Usage:
    python3 whisper_server.py [--port 13000] [--model small]

Models (smallest to largest):
    tiny, base, small, medium, large-v1, large-v2, large-v3
"""

import argparse
import io
import logging
import time
from typing import Optional

from fastapi import FastAPI, HTTPException, Request
from pydantic import BaseModel
from faster_whisper import WhisperModel

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("whisper-server")

app = FastAPI(title="Local Whisper ASR Server")

# Global model instance
model = None
model_size = None
device = "auto"
compute_type = "float16"  # Use int8 for slower machines


def load_model(size: str):
    global model, model_size, compute_type
    model_size = size

    # Use int8 for Mac without GPU, float16 if GPU available
    compute_type = "float32"
    logger.info(f"Using float32 for maximum accuracy (CTranslate2 CPU)")

    logger.info(f"Loading Whisper model: {size} (compute_type={compute_type})")
    model = WhisperModel(size, device=device, compute_type=compute_type, download_root="/tmp/whisper-models")
    logger.info(f"Model loaded successfully")


class TranscriptionRequest(BaseModel):
    file: Optional[bytes] = None
    language: Optional[str] = None
    task: Optional[str] = "transcribe"
    temperature: Optional[float] = 0.0


@app.on_event("startup")
async def startup_event():
    # Model already loaded in main(), skip here to avoid reloading
    logger.info(f"Whisper model {model_size} already loaded, skipping reload")
    pass


@app.post("/v1/audio/transcriptions")
async def transcribe(request: Request):
    """OpenAI-compatible transcription endpoint."""
    if model is None:
        raise HTTPException(status_code=503, detail="Model not loaded")

    try:
        # Parse multipart form data
        form = await request.form()
        audio_file = form.get("file")
        if audio_file is None:
            raise HTTPException(status_code=400, detail="No audio file provided")

        audio_bytes = await audio_file.read()
        language = form.get("language", "auto")
        task = form.get("task", "transcribe")

        # Transcribe audio
        segments, info = model.transcribe(
            io.BytesIO(audio_bytes),
            language=language if language != "auto" else None,
            task=task,
            temperature=0.0,
            vad_filter=True,
        )

        text = "".join(segment.text for segment in segments)
        text = text.strip()

        logger.info(f"Transcribed ({info.language}): {text[:100]}...")

        return {
            "text": text,
            "language": info.language,
            "duration": info.duration,
        }

    except Exception as e:
        logger.error(f"Transcription error: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@app.get("/health")
async def health():
    return {
        "status": "ok",
        "model": model_size,
        "compute_type": compute_type,
    }


if __name__ == "__main__":
    import os
    parser = argparse.ArgumentParser(description="Local Whisper ASR Server")
    parser.add_argument("--port", type=int, default=13000, help="Port to listen on")
    parser.add_argument("--model", type=str, default="small", help="Whisper model size")
    args = parser.parse_args()

    load_model(args.model)
    logger.info(f"Starting Whisper server on port {args.port}")
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=args.port)
