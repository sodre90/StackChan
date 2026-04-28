# Use official ESP-IDF v5.5 Docker image
FROM espressif/idf:v5.5.4

# Set working directory
WORKDIR /workspace

# Install additional dependencies needed for fetching repos
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    python3 \
    && rm -rf /var/lib/apt/lists/*

# Set IDF environment
ENV IDF_PATH=/opt/esp/idf

# Copy firmware source
COPY . /workspace

# Clone/update dependencies (will be cached if nothing changed)
RUN python3 fetch_repos.py

# Default command: build the firmware
CMD ["idf.py", "build"]
