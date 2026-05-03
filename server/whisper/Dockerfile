FROM nvidia/cuda:12.4.1-cudnn-runtime-ubuntu22.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        python3 python3-pip ffmpeg \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
RUN pip3 install --no-cache-dir \
        "faster-whisper>=1.0.3" \
        fastapi uvicorn pydantic python-multipart

COPY whisper_server.py .
EXPOSE 13000
CMD ["python3", "whisper_server.py", \
     "--model", "large-v3", \
     "--device", "cuda", \
     "--compute-type", "int8_float16", \
     "--port", "13000"]
