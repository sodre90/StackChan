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
    initial_prompt = form.get("prompt") or None

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
            beam_size=10,
            vad_filter=True,
            condition_on_previous_text=False,
            initial_prompt=initial_prompt,
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
    parser.add_argument("--model-subfolder", type=str, default=None,
                        help="Subfolder within the HF repo containing a pre-quantized CTranslate2 model "
                             "(e.g. quants/int8_bfloat16). Skips runtime conversion.")
    parser.add_argument("--feature-size", type=int, default=None,
                        help="Override mel filterbank size (80 for old models, 128 for large-v3+). "
                             "Auto-detected when omitted.")
    args = parser.parse_args()

    model_path = args.model

    if args.model_subfolder:
        from huggingface_hub import snapshot_download
        cache_key = args.model.replace("/", "__") + "__" + args.model_subfolder.replace("/", "__")
        local_dir = os.path.join(os.path.expanduser("~"), ".cache", "ct2_models", cache_key)
        subfolder_path = os.path.join(local_dir, args.model_subfolder)
        if not os.path.exists(os.path.join(subfolder_path, "model.bin")):
            logger.info(f"Downloading {args.model}/{args.model_subfolder} from HuggingFace...")
            snapshot_download(repo_id=args.model, local_dir=local_dir,
                              allow_patterns=[f"{args.model_subfolder}/*"])
            logger.info("Download done.")
        model_path = subfolder_path
        logger.info(f"Using pre-quantized model at {model_path}")
    else:
        ct2_cache = os.path.join(os.path.expanduser("~"), ".cache", "ct2_models",
                                 args.model.replace("/", "__"))
        if not os.path.exists(os.path.join(ct2_cache, "model.bin")):
            try:
                from ctranslate2.converters import TransformersConverter
                logger.info(f"Converting {args.model} to CTranslate2 format → {ct2_cache}")
                TransformersConverter(args.model, low_cpu_mem_usage=True).convert(
                    ct2_cache, quantization=args.compute_type, force=True)
                model_path = ct2_cache
                logger.info("Conversion done.")
            except Exception as e:
                logger.warning(f"CTranslate2 conversion failed: {e}")

    logger.info(f"Loading {model_path} on {args.device} ({args.compute_type})...")
    model = WhisperModel(model_path, device=args.device, compute_type=args.compute_type, cpu_threads=8)

    if args.feature_size is not None:
        from faster_whisper.feature_extractor import FeatureExtractor
        model.feature_extractor = FeatureExtractor(feature_size=args.feature_size)
        logger.info(f"Feature extractor overridden: {args.feature_size} mel bins")

    logger.info("Model loaded.")

    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=args.port)
