/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include "secret_logic.h"

#include "settings.h"

namespace secret_logic {

__attribute__((weak)) std::string get_server_url()
{
    return "http://192.168.1.160:12800";
}

__attribute__((weak)) std::string generate_auth_token()
{
    // Read the shared bearer token provisioned via the OTA response and persisted
    // to NVS by the firmware's OTA handler. The same token gates the AI WebSocket
    // and the relay's robot side; returns "" until the device has been OTA-provisioned.
    Settings settings("websocket", false);
    return settings.GetString("token");
}

__attribute__((weak)) std::string generate_handshake_token(std::string_view data)
{
    return "hi-stack-chan";
}

}  // namespace secret_logic
