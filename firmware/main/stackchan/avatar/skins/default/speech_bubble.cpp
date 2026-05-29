/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include "default.h"

using namespace uitk;
using namespace uitk::lvgl_cpp;
using namespace stackchan::avatar;

// Multi-line speech bubble anchored to the bottom of the 320x240 panel. The
// bubble grows upward from the bottom as the wrapped text gets taller, up to
// _bubble_max_height. When the reply is taller than that cap, the text no
// longer fits, so it pages through: a timer flips to the next page of lines
// every _page_dwell_ms (a single redraw per flip — no continuous animation, so
// it doesn't lag the UI), pausing on each page, then wrapping back to the top.
static const int _bottom_margin    = 4;    // gap between bubble and screen bottom
static const int _text_mx          = 16;   // horizontal text inset
static const int _text_vpad        = 10;   // vertical text inset (top+bottom each)
static const int _bubble_width     = 300;
static const int _bubble_min_height = 44;  // one line
static const int _bubble_max_height = 110; // ~4 lines of montserrat_16
static const int _bubble_radius    = 18;
static const Vector2i _container_size = Vector2i(320, _bubble_max_height + _bottom_margin);

static const uint32_t _page_dwell_ms     = 3500; // time each page is held before flipping
static const uint32_t _render_debounce_ms = 120;  // coalesce a burst of cumulative updates

LV_IMAGE_DECLARE(default_bubble_arrow);

DefaultSpeechBubble::DefaultSpeechBubble(lv_obj_t* parent, lv_color_t primaryColor, lv_color_t secondaryColor,
                                         const lv_font_t* font)
{
    _container = std::make_unique<Container>(parent);
    _container->setRadius(0);
    _container->setAlign(LV_ALIGN_BOTTOM_MID);
    _container->setBorderWidth(0);
    _container->setBgOpa(0);
    _container->removeFlag(LV_OBJ_FLAG_SCROLLABLE);
    _container->setSize(_container_size.x, _container_size.y);
    _container->setPos(0, 0);
    _container->setPadding(0, 0, 0, 0);

    // The downward-pointing arrow doesn't fit a bottom-anchored multi-line
    // bubble, so it's created (header owns the member) but kept hidden.
    _arrow = std::make_unique<Image>(_container->get());
    _arrow->setSrc(&default_bubble_arrow);
    _arrow->setImageRecolorOpa(LV_OPA_COVER);
    _arrow->setImageRecolor(primaryColor);
    _arrow->setHidden(true);

    _bubble = std::make_unique<Container>(_container->get());
    _bubble->setRadius(_bubble_radius);
    _bubble->setAlign(LV_ALIGN_BOTTOM_MID);
    _bubble->setBorderWidth(0);
    _bubble->setBgColor(primaryColor);
    _bubble->removeFlag(LV_OBJ_FLAG_SCROLLABLE);
    _bubble->setSize(_bubble_width, _bubble_min_height);
    _bubble->setPos(0, -_bottom_margin);
    // Vertical padding forms the visible text margin; the label slides within it.
    lv_obj_set_style_pad_top(_bubble->get(), _text_vpad, 0);
    lv_obj_set_style_pad_bottom(_bubble->get(), _text_vpad, 0);

    _text = std::make_unique<Label>(_bubble->get());
    _text->setTextColor(secondaryColor);
    _text->setTextFont(font);
    _text->setTextAlign(LV_TEXT_ALIGN_CENTER);
    // Top-anchored so paging starts from the first line; line justification
    // stays centered via setTextAlign above.
    _text->setAlign(LV_ALIGN_TOP_MID);
    _text->setPos(0, 0);
    _text->setWidth(_bubble_width - _text_mx * 2);
    _text->setLongMode(LV_LABEL_LONG_MODE_WRAP);

    clearSpeech();
}

DefaultSpeechBubble::~DefaultSpeechBubble()
{
    if (_render_timer != nullptr) {
        lv_timer_delete(_render_timer);
        _render_timer = nullptr;
    }
    stopPaging();
    _text.reset();
    _bubble.reset();
    _arrow.reset();
    _container.reset();
}

void DefaultSpeechBubble::stopPaging()
{
    if (_page_timer != nullptr) {
        lv_timer_delete(_page_timer);
        _page_timer = nullptr;
    }
    _page_pos = 0;
    if (_text) {
        lv_obj_set_style_translate_y(_text->get(), 0, 0);
    }
}

void DefaultSpeechBubble::pageTimerCb(lv_timer_t* timer)
{
    auto* self = static_cast<DefaultSpeechBubble*>(lv_timer_get_user_data(timer));
    if (self != nullptr) {
        self->advancePage();
    }
}

void DefaultSpeechBubble::advancePage()
{
    int next = _page_pos + _page_step;
    if (_page_pos >= _scroll_max) {
        next = 0;  // already at the bottom page → wrap to the top
    } else if (next > _scroll_max) {
        next = _scroll_max;  // last page: clamp so the final lines sit flush
    }
    _page_pos = next;
    lv_obj_set_style_translate_y(_text->get(), -_page_pos, 0);
}

void DefaultSpeechBubble::setSpeech(std::string_view text)
{
    if (text.empty()) {
        clearSpeech();
        return;
    }

    // Coalesce the rapid burst of cumulative updates: store the latest text and
    // (re)arm a short timer. Only the last update in a burst actually renders,
    // so audio playback isn't stuttered by a storm of paragraph re-layouts.
    _pending_text.assign(text);
    if (_render_timer == nullptr) {
        _render_timer = lv_timer_create(renderTimerCb, _render_debounce_ms, this);
    } else {
        lv_timer_reset(_render_timer);
    }
}

void DefaultSpeechBubble::renderTimerCb(lv_timer_t* timer)
{
    auto* self = static_cast<DefaultSpeechBubble*>(lv_timer_get_user_data(timer));
    if (self != nullptr) {
        self->renderPending();
    }
}

void DefaultSpeechBubble::renderPending()
{
    // One-shot: stop the debounce timer until the next setSpeech().
    if (_render_timer != nullptr) {
        lv_timer_delete(_render_timer);
        _render_timer = nullptr;
    }

    const std::string& text = _pending_text;

    // Reset any paging from the previous reply.
    stopPaging();

    _text->setText(text.c_str());

    // Measure the text wrapped to the bubble's inner width, then size the
    // bubble's height to fit (clamped). It's bottom-anchored, so it grows
    // upward toward the avatar's face as the reply gets longer.
    const int wrap_width = _bubble_width - _text_mx * 2;
    lv_point_t text_size;
    lv_text_get_size(&text_size, text.data(), _text->getTextFont(), 0, 0, wrap_width, LV_TEXT_FLAG_NONE);

    const int full_height = text_size.y + _text_vpad * 2;
    int bubble_height     = min(full_height, _bubble_max_height);
    bubble_height         = max(bubble_height, _bubble_min_height);

    _bubble->setSize(_bubble_width, bubble_height);

    // If the reply is taller than the (capped) bubble, the bottom is clipped.
    // Page through it: step by whole lines so a line is never split across two
    // pages, hold each page, then wrap. The visible window is bubble minus vpad.
    const int visible_h = bubble_height - _text_vpad * 2;
    _scroll_max         = text_size.y - visible_h;
    if (_scroll_max > 0) {
        const int line_h     = lv_font_get_line_height(_text->getTextFont());
        int lines_per_page   = line_h > 0 ? visible_h / line_h : 1;
        if (lines_per_page < 1) {
            lines_per_page = 1;
        }
        _page_step  = lines_per_page * line_h;
        _page_timer = lv_timer_create(pageTimerCb, _page_dwell_ms, this);
    }

    setVisible(true);
}

void DefaultSpeechBubble::clearSpeech()
{
    if (_render_timer != nullptr) {
        lv_timer_delete(_render_timer);
        _render_timer = nullptr;
    }
    _pending_text.clear();
    stopPaging();
    _text->setText("");
    setVisible(false);
}

void DefaultSpeechBubble::setVisible(bool visible)
{
    SpeechBubble::setVisible(visible);

    _container->setHidden(!visible);
}

void DefaultSpeechBubble::setTextFont(void* font)
{
    if (_text && font) {
        _text->setTextFont((lv_font_t*)font);
    }
}
