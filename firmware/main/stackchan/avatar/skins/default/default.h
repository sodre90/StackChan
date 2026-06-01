/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#pragma once
#include "../../avatar/avatar.h"
#include "../../avatar/elements/feature.h"
#include <lvgl.h>
#include <smooth_lvgl.hpp>
#include <memory>
#include <string>

namespace stackchan::avatar {

/**
 * @brief
 *
 */
class DefaultAvatar : public Avatar {
public:
    lv_color_t primaryColor   = lv_color_white();
    lv_color_t secondaryColor = lv_color_black();

    void init(lv_obj_t* parent, const lv_font_t* font = &lv_font_montserrat_16);
    uitk::lvgl_cpp::Container* getPanel() const;

private:
    std::unique_ptr<uitk::lvgl_cpp::Container> _pannel;
};

/**
 * @brief
 *
 */
class DefaultEyes : public Feature {
public:
    DefaultEyes(lv_obj_t* parent, lv_color_t primaryColor, lv_color_t secondaryColor, bool isLeftEye);
    ~DefaultEyes();

    void setPosition(const uitk::Vector2i& position) override;
    void setWeight(int weight) override;
    void setRotation(int rotation) override;
    void setEmotion(const Emotion& emotion) override;
    void setVisible(bool visible) override;
    void setSize(int size) override;

private:
    bool _is_left_eye    = false;
    int _eyelid_offset_y = 0;

    std::unique_ptr<uitk::lvgl_cpp::Container> _container;
    std::unique_ptr<uitk::lvgl_cpp::Container> _eye;
    std::unique_ptr<uitk::lvgl_cpp::Container> _eyelid;
};

/**
 * @brief
 *
 */
class DefaultMouth : public Feature {
public:
    DefaultMouth(lv_obj_t* parent, lv_color_t primaryColor, lv_color_t secondaryColor);
    ~DefaultMouth();

    void setPosition(const uitk::Vector2i& position) override;
    void setWeight(int weight) override;
    void setRotation(int rotation) override;
    void setVisible(bool visible) override;

private:
    std::unique_ptr<uitk::lvgl_cpp::Container> _mouth;
};

/**
 * @brief
 *
 */
class DefaultSpeechBubble : public SpeechBubble {
public:
    DefaultSpeechBubble(lv_obj_t* parent, lv_color_t primaryColor, lv_color_t secondaryColor, const lv_font_t* font);
    ~DefaultSpeechBubble();

    void setSpeech(std::string_view text) override;
    void clearSpeech() override;
    void setVisible(bool visible) override;
    void setTextFont(void* font) override;

private:
    // Cumulative replies stream in as a rapid burst of ever-longer sentence_start
    // messages. Re-laying out and repainting the growing paragraph on every one
    // (right as audio playback starts) stutters the audio. Debounce instead: each
    // setSpeech() just stores the text and resets a short timer; only the final
    // text in a burst is actually rendered.
    void renderPending();
    static void renderTimerCb(lv_timer_t* timer);

    // Page through a reply too tall for the bubble: a periodic timer steps the
    // text by one page (single redraw per flip, no continuous animation, so it
    // doesn't lag the UI), pausing on each page, then wrapping to the top.
    void stopPaging();
    void advancePage();
    static void pageTimerCb(lv_timer_t* timer);

    std::unique_ptr<uitk::lvgl_cpp::Container> _container;
    std::unique_ptr<uitk::lvgl_cpp::Image> _arrow;
    std::unique_ptr<uitk::lvgl_cpp::Container> _bubble;
    std::unique_ptr<uitk::lvgl_cpp::Label> _text;

    std::string _pending_text;
    lv_timer_t* _render_timer = nullptr;

    lv_timer_t* _page_timer = nullptr;
    int _page_pos            = 0;  // current vertical offset (px, >= 0)
    int _page_step           = 0;  // whole-line page height (px)
    int _scroll_max          = 0;  // max offset = content height - visible height
    int _prev_content_height = 0;  // height of previous cumulative text (px)
};

}  // namespace stackchan::avatar
