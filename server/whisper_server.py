#!/usr/bin/env python3
"""
Local Whisper ASR server using faster-whisper (CUDA / CPU).
Exposes an OpenAI-compatible /v1/audio/transcriptions endpoint.

Usage:
    python3 whisper_server.py [--port 13000] [--model large-v3] \\
                              [--device cuda] [--compute-type float16]
"""

import argparse
import logging
import os
import tempfile
from typing import Optional

from faster_whisper import WhisperModel
from fastapi import FastAPI, HTTPException, Request

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("whisper-server")

app = FastAPI(title="Local Whisper ASR Server")

model: Optional[WhisperModel] = None


@app.post("/v1/audio/transcriptions")
async def transcribe(request: Request):
    if model is None:
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
        segments, info = model.transcribe(
            tmp_path,
            language=language,
            task=task,
            beam_size=5,
            vad_filter=False,
        )
        text = " ".join(seg.text for seg in segments).strip()
    finally:
        os.unlink(tmp_path)

    detected_lang = info.language
    logger.info(f"Transcribed ({detected_lang}, {info.duration:.2f}s): {text[:100]}")

    return {
        "text": text,
        "language": detected_lang,
        "duration": info.duration,
    }


@app.get("/health")
async def health():
    return {"status": "ok"}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Local Whisper ASR Server (faster-whisper)")
    parser.add_argument("--port", type=int, default=13000)
    parser.add_argument("--model", type=str, default="large-v3",
                        help="tiny, base, small, medium, large-v1, large-v2, large-v3, large-v3-turbo, "
                             "or a HuggingFace repo id")
    parser.add_argument("--device", type=str, default="cuda",
                        choices=["cuda", "cpu", "auto"])
    parser.add_argument("--compute-type", type=str, default="float16",
                        help="float16 (cuda), int8_float16 (cuda, faster), int8 (cpu)")
    args = parser.parse_args()

    logger.info(f"Loading {args.model} on {args.device} ({args.compute_type})...")
    model = WhisperModel(args.model, device=args.device, compute_type=args.compute_type)
    logger.info("Model loaded.")

    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=args.port)
