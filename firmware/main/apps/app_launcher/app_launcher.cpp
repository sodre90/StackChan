/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include "app_launcher.h"
#include <hal/hal.h>
#include <mooncake.h>
#include <mooncake_log.h>
#include <stackchan/stackchan.h>
#include <cstdint>

using namespace mooncake;

void AppLauncher::onLauncherCreate()
{
    mclog::tagInfo(getAppInfo().name, "on create");

    // 打开自己
    open();
}

void AppLauncher::onLauncherOpen()
{
    mclog::tagInfo(getAppInfo().name, "on open");

    LvglLockGuard lock;

    if (!_startup_checked && !GetHAL().isAppConfiged()) {
        mclog::tagInfo(getAppInfo().name, "app not configured, start startup worker");
        _startup_worker = std::make_unique<setup_workers::StartupWorker>();
    } else {
        create_launcher_view();
    }
}

void AppLauncher::onLauncherRunning()
{
    LvglLockGuard lock;

    if (_startup_worker) {
        _startup_worker->update();
        if (_startup_worker->isDone()) {
            _startup_worker.reset();
            _startup_checked = true;
            create_launcher_view();
        }
    } else {
        _view->update();
        auto_agent_update();
        screensaver_update();
    }

    GetStackChan().update();
}

void AppLauncher::onLauncherClose()
{
    mclog::tagInfo(getAppInfo().name, "on close");

    LvglLockGuard lock;

    _view.reset();
}

void AppLauncher::onLauncherDestroy()
{
    mclog::tagInfo(getAppInfo().name, "on close");
}

void AppLauncher::create_launcher_view()
{
    _view = std::make_unique<view::LauncherView>();
    _view->init(getAppProps());
    _view->onAppClicked = [&](int appID) {
        mclog::tagInfo(getAppInfo().name, "handle open app, app id: {}", appID);
        _auto_agent_armed = false;  // explicit app choice cancels auto-enter
        openApp(appID);
    };

    // Arm the auto-enter only on an already-configured device so a fresh
    // device still goes through WiFi setup instead of jumping into xiaozhi.
    _auto_agent_armed  = GetHAL().isAppConfiged();
    _user_interacted   = false;
    _launcher_open_ms  = GetHAL().millis();
}

void AppLauncher::auto_agent_update()
{
    // Show the launcher briefly after boot, then enter the AI agent (xiaozhi)
    // unless the user touches the screen first. A single touch cancels it so
    // the launcher stays usable for picking other apps / Setup.
    const uint32_t AUTO_ENTER_MS    = 3000;  // launcher dwell before auto-enter
    const uint32_t TOUCH_GRACE_MS   = 600;   // ignore the boot-time inactive baseline
    const uint32_t TOUCH_ACTIVE_MS  = 300;   // inactive time below this == fresh touch

    if (!_auto_agent_armed) {
        return;
    }

    const uint32_t elapsed = GetHAL().millis() - _launcher_open_ms;

    // After a short grace period, a low inactivity time means the user just
    // touched the screen → cancel auto-enter and let them browse.
    if (elapsed > TOUCH_GRACE_MS && lv_display_get_inactive_time(NULL) < TOUCH_ACTIVE_MS) {
        _auto_agent_armed = false;
        mclog::tagInfo(getAppInfo().name, "auto-enter cancelled by touch");
        return;
    }

    if (elapsed >= AUTO_ENTER_MS) {
        _auto_agent_armed = false;
        mclog::tagInfo(getAppInfo().name, "auto-entering AI agent (xiaozhi)");
        GetHAL().requestXiaozhiStart();
    }
}

void AppLauncher::screensaver_update()
{
    const uint32_t SCREENSAVER_TIMEOUT_MS = 30000;

    uint32_t idle_time = lv_display_get_inactive_time(NULL);
    if (idle_time >= SCREENSAVER_TIMEOUT_MS) {
        if (!_screensaver) {
            _screensaver = std::make_unique<view::Screensaver>();
            _screensaver->init();
        }
    } else if (_screensaver) {
        _screensaver.reset();
    }

    // Update in 30ms interval
    if (_screensaver && GetHAL().millis() - _screensaver_timecount > 30) {
        _screensaver_timecount = GetHAL().millis();
        _screensaver->update();
    }
}
