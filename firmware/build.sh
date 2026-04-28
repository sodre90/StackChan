#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE_NAME="stackchan-firmware"
CONTAINER_NAME="stackchan-build"

# Check if Docker is running
if ! docker info >/dev/null 2>&1; then
    echo "❌ Docker is not running. Please start Docker Desktop first."
    exit 1
fi

# Build image if it doesn't exist
if ! docker image inspect "${IMAGE_NAME}" >/dev/null 2>&1; then
    echo "📦 Building Docker image (first time, may take a few minutes)..."
    docker build -t "${IMAGE_NAME}" .
fi

# Find ESP32 serial port (macOS)
find_esp32_port() {
    # Look for common ESP32 USB serial patterns
    for port in $(ls /dev/cu.usbserial* /dev/cu.usbmodem* 2>/dev/null); do
        echo "$port"
    done
}

case "${1:-build}" in
    build)
        echo "🔨 Building firmware in Docker..."
        docker run --rm \
            -v "${SCRIPT_DIR}:/workspace" \
            -w /workspace \
            -e IDF_PATH=/opt/esp/idf \
            --name "${CONTAINER_NAME}" \
            "${IMAGE_NAME}" \
            idf.py build
        echo "✅ Build complete. Binaries in build/"
        ;;

    clean)
        echo "🧹 Cleaning build artifacts..."
        docker run --rm \
            -v "${SCRIPT_DIR}:/workspace" \
            -w /workspace \
            -e IDF_PATH=/opt/esp/idf \
            --name "${CONTAINER_NAME}" \
            "${IMAGE_NAME}" \
            idf.py fullclean
        echo "✅ Clean complete."
        ;;

    menuconfig)
        echo "⚙️  Opening menuconfig..."
        docker run --rm -it \
            -v "${SCRIPT_DIR}:/workspace" \
            -w /workspace \
            -e IDF_PATH=/opt/esp/idf \
            --name "${CONTAINER_NAME}" \
            "${IMAGE_NAME}" \
            idf.py menuconfig
        ;;

    flash)
        PORT="${2:-}"
        if [ -z "${PORT}" ]; then
            echo "❌ Please specify the serial port, e.g.:"
            echo "   ./build.sh flash /dev/cu.usbmodem1201"
            echo ""
            echo "Available ESP32 ports:"
            find_esp32_port | while read p; do echo "  $p"; done
            echo ""
            echo "If none listed, connect your ESP32 via USB and try again."
            exit 1
        fi
        echo "🔌 Flashing firmware to ${PORT}..."
        esptool.py --chip esp32s3 -p "${PORT}" -b 460800 \
            --before=default_reset --after=hard_reset \
            write_flash \
            --flash_mode dio --flash_freq 80m --flash_size 16MB \
            0x0 "${SCRIPT_DIR}/build/bootloader/bootloader.bin" \
            0x8000 "${SCRIPT_DIR}/build/partition_table/partition-table.bin" \
            0xd000 "${SCRIPT_DIR}/build/ota_data_initial.bin" \
            0x20000 "${SCRIPT_DIR}/build/stack-chan.bin" \
            0xa00000 "${SCRIPT_DIR}/build/generated_assets.bin"
        echo "✅ Flash complete."
        ;;

    monitor)
        PORT="${2:-}"
        if [ -z "${PORT}" ]; then
            echo "❌ Please specify the serial port, e.g.:"
            echo "   ./build.sh monitor /dev/cu.usbmodem1201"
            exit 1
        fi
        echo "📟 Opening serial monitor on ${PORT}..."
        esptool.py --port "${PORT}" read_mac 2>/dev/null || true
        exec socat - "UNIX-CONNECT:${PORT}" 2>/dev/null || \
            screen "${PORT}" 921600 2>/dev/null || \
            cat < "${PORT}" 2>/dev/null || \
            echo "Install 'screen' or 'socat' for serial monitoring."
        ;;

    shell)
        echo "💻 Opening interactive shell..."
        docker run --rm -it \
            -v "${SCRIPT_DIR}:/workspace" \
            -w /workspace \
            -e IDF_PATH=/opt/esp/idf \
            --name "${CONTAINER_NAME}" \
            "${IMAGE_NAME}" \
            /bin/bash
        ;;

    *)
        echo "Usage: $0 {build|clean|menuconfig|flash|monitor|shell}"
        echo ""
        echo "  build       Build the firmware in Docker (default)"
        echo "  clean       Clean all build artifacts"
        echo "  menuconfig  Open ESP-IDF menuconfig (in Docker)"
        echo "  flash       Flash firmware to ESP32 via native idf.py (requires port)"
        echo "  monitor     Open serial monitor via native idf.py (requires port)"
        echo "  shell       Open interactive shell in Docker container"
        echo ""
        echo "Note: Flashing and monitoring use native ESP-IDF tools on macOS."
        echo "      Building uses Docker for a consistent build environment."
        exit 1
        ;;
esac
