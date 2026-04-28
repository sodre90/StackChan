
## Build

### Fetch Dependencies

The `xiaozhi-esp32` component is a git submodule. All other components are fetched by script.

```bash
# 1. Pull xiaozhi-esp32 (our fork, stackchan branch)
git submodule update --init --recursive

# 2. Fetch remaining components (mooncake, ArduinoJson, esp-now, etc.)
python3 ./fetch_repos.py
```

### Configure Server Address

Before building, set your server's local IP in two places:

**`main/Kconfig.projbuild`**
```
default "http://YOUR_SERVER_IP:12800/xiaozhi/ota/"
```

**`main/hal/utils/secret_logic/secret_logic.cpp`**
```cpp
return "http://YOUR_SERVER_IP:12800";
```

### Build (Docker, recommended)

```bash
# Builds firmware in a Docker container — no local ESP-IDF install needed
./build.sh build

# Other commands:
./build.sh clean       # clean build artifacts
./build.sh menuconfig  # open ESP-IDF menuconfig
./build.sh shell       # open interactive shell in container
```

### Flash

```bash
./build.sh flash /dev/cu.usbmodem1201   # replace with your device port
```

### Build (native ESP-IDF)

Requires [ESP-IDF v5.5.4](https://docs.espressif.com/projects/esp-idf/en/v5.5.4/esp32s3/index.html).

```bash
idf.py build
idf.py flash
```

---

For the full server-side (AI pipeline) setup see [server/LOCAL_AI_SETUP.md](../server/LOCAL_AI_SETUP.md).
