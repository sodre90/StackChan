#!/usr/bin/env python3
"""
Local Whisper ASR server using mlx-whisper (Apple Silicon / Metal GPU).
Exposes an OpenAI-compatible /v1/audio/transcriptions endpoint.

Usage:
    python3 whisper_server.py [--port 13000] [--model large-v3-turbo]

Models: tiny, base, small, medium, large-v1, large-v2, large-v3, large-v3-turbo
"""

import argparse
import io
import logging
import tempfile
import os

import mlx_whisper
from fastapi import FastAPI, HTTPException, Request

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("whisper-server")

app = FastAPI(title="Local Whisper ASR Server")

model_path = None  # set at startup

MLX_MODEL_MAP = {
    "tiny":             "mlx-community/whisper-tiny-mlx-q4",
    "base":             "mlx-community/whisper-base-mlx-q4",
    "small":            "mlx-community/whisper-small-mlx-q4",
    "medium":           "mlx-community/whisper-medium-mlx-q4",
    "large-v1":         "mlx-community/whisper-large-v1-mlx",
    "large-v2":         "mlx-community/whisper-large-v2-mlx",
    "large-v3":         "mlx-community/whisper-large-v3-mlx",
    "large-v3-turbo":   "mlx-community/whisper-large-v3-turbo",
}


def resolve_model(name: str) -> str:
    if "/" in name:
        return name  # already a HuggingFace repo id
    resolved = MLX_MODEL_MAP.get(name)
    if resolved is None:
        raise ValueError(f"Unknown model '{name}'. Known: {list(MLX_MODEL_MAP)}")
    return resolved


@app.post("/v1/audio/transcriptions")
async def transcribe(request: Request):
    if model_path is None:
        raise HTTPException(status_code=503, detail="Model not loaded")

    form = await request.form()
    audio_file = form.get("file")
    if audio_file is None:
        raise HTTPException(status_code=400, detail="No audio file provided")

    audio_bytes = await audio_file.read()
    language = form.get("language") or None
    if language == "auto":
        language = None
    task = form.get("task", "transcribe")

    # mlx_whisper.transcribe needs a file path, not bytes
    suffix = ".wav"
    filename = getattr(audio_file, "filename", None)
    if filename:
        _, ext = os.path.splitext(filename)
        if ext:
            suffix = ext

    with tempfile.NamedTemporaryFile(suffix=suffix, delete=False) as tmp:
        tmp.write(audio_bytes)
        tmp_path = tmp.name

    try:
        result = mlx_whisper.transcribe(
            tmp_path,
            path_or_hf_repo=model_path,
            temperature=0.0,
            language=language,
            task=task,
        )
    finally:
        os.unlink(tmp_path)

    text = result.get("text", "").strip()
    detected_lang = result.get("language", language or "unknown")
    logger.info(f"Transcribed ({detected_lang}): {text[:100]}")

    return {
        "text": text,
        "language": detected_lang,
        "duration": result.get("duration", 0.0),
    }


@app.get("/health")
async def health():
    return {"status": "ok", "model": model_path}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Local Whisper ASR Server (mlx)")
    parser.add_argument("--port", type=int, default=13000)
    parser.add_argument("--model", type=str, default="large-v3-turbo",
                        help="Model name or HuggingFace repo id")
    args = parser.parse_args()

    model_path = resolve_model(args.model)
    logger.info(f"Using mlx-whisper model: {model_path}")

    # warm-up: mlx_whisper downloads/caches the model on first transcribe call
    logger.info("Model will be downloaded on first request if not cached.")

    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=args.port)
