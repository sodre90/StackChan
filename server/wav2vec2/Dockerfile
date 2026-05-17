FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        libsndfile1 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Install torch CPU separately first (large, benefits from layer caching)
RUN pip3 install --no-cache-dir torch --index-url https://download.pytorch.org/whl/cpu

RUN pip3 install --no-cache-dir \
        transformers soundfile \
        fastapi uvicorn pydantic python-multipart

COPY wav2vec2_server.py .
EXPOSE 13000
CMD ["python3", "wav2vec2_server.py", "--port", "13000"]
