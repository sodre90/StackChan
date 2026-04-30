FROM python:3.12-slim
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir fastapi uvicorn edge-tts
COPY tts_server.py .
EXPOSE 14000
CMD ["python3", "tts_server.py", "--port", "14000"]
