FROM python:3.12-slim
WORKDIR /app
RUN pip install --no-cache-dir fastapi uvicorn faster-whisper pydantic python-multipart
COPY whisper_server.py .
EXPOSE 13000
CMD ["python3", "whisper_server.py", "--model", "large-v3", "--port", "13000"]
