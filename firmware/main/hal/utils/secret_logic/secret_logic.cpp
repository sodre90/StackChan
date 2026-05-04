/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include "secret_logic.h"

namespace secret_logic {

__attribute__((weak)) std::string get_server_url()
{
    return "http://192.168.88.50:12800";
}

__attribute__((weak)) std::string generate_auth_token()
{
    return "hi-stack-chan";
}

__attribute__((weak)) std::string generate_handshake_token(std::string_view data)
{
    return "hi-stack-chan";
}

}  // namespace secret_logic
