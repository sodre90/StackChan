/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#pragma once
#include "view/view.h"
#include <apps/app_setup/workers/workers.h>
#include <mooncake.h>
#include <mooncake_templates.h>
#include <cstdint>
#include <memory>

class AppLauncher : public mooncake::templates::AppLauncherBase {
public:
    void onLauncherCreate() override;
    void onLauncherOpen() override;
    void onLauncherRunning() override;
    void onLauncherClose() override;
    void onLauncherDestroy() override;

private:
    std::unique_ptr<view::LauncherView> _view;
    std::unique_ptr<view::Screensaver> _screensaver;
    std::unique_ptr<setup_workers::StartupWorker> _startup_worker;
    uint32_t _screensaver_timecount = 0;
    bool _startup_checked           = false;

    // Auto-enter the AI agent (xiaozhi) shortly after boot unless the user
    // touches the launcher first. Armed only when the device is configured.
    bool _auto_agent_armed   = false;
    bool _user_interacted    = false;
    uint32_t _launcher_open_ms = 0;

    void create_launcher_view();
    void auto_agent_update();
    void screensaver_update();
};
