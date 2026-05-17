#!/usr/bin/env python3
"""
Hungarian ASR server using wav2vec2-large-xlsr-53-hungarian.
Exposes an OpenAI-compatible /v1/audio/transcriptions endpoint.
"""

import argparse
import logging
import os
import tempfile

import numpy as np
import soundfile as sf
import torch
from fastapi import FastAPI, HTTPException, Request
from transformers import Wav2Vec2ForCTC, Wav2Vec2Processor
import uvicorn

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("wav2vec2-server")

app = FastAPI(title="Hungarian Wav2Vec2 ASR Server")

processor = None
model = None
SAMPLE_RATE = 16000


@app.post("/v1/audio/transcriptions")
async def transcribe(request: Request):
    if model is None:
        raise HTTPException(status_code=503, detail="Model not loaded")

    form = await request.form()
    audio_file = form.get("file")
    if audio_file is None:
        raise HTTPException(status_code=400, detail="No audio file provided")

    audio_bytes = await audio_file.read()

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
        audio, sr = sf.read(tmp_path, dtype="float32")
        if audio.ndim > 1:
            audio = audio.mean(axis=1)

        duration = len(audio) / sr

        if sr != SAMPLE_RATE:
            target_length = int(len(audio) * SAMPLE_RATE / sr)
            audio = np.interp(
                np.linspace(0, len(audio), target_length),
                np.arange(len(audio)),
                audio,
            ).astype(np.float32)

        inputs = processor(audio, sampling_rate=SAMPLE_RATE, return_tensors="pt", padding=True)

        with torch.no_grad():
            logits = model(**inputs).logits

        predicted_ids = torch.argmax(logits, dim=-1)
        text = processor.batch_decode(predicted_ids)[0].strip()
    finally:
        os.unlink(tmp_path)

    logger.info(f"Transcribed ({duration:.2f}s): {text[:100]}")

    return {
        "text": text,
        "language": "hu",
        "duration": duration,
    }


@app.get("/health")
async def health():
    return {"status": "ok"}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=13000)
    parser.add_argument("--model", type=str, default="jonatasgrosman/wav2vec2-large-xlsr-53-hungarian")
    args = parser.parse_args()

    logger.info(f"Loading {args.model}...")
    processor = Wav2Vec2Processor.from_pretrained(args.model)
    model = Wav2Vec2ForCTC.from_pretrained(args.model)
    model.eval()
    torch.set_num_threads(min(8, os.cpu_count() or 8))
    logger.info("Model loaded.")

    uvicorn.run(app, host="0.0.0.0", port=args.port)
