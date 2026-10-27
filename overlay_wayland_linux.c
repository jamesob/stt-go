//go:build linux

// Recording overlay for Wayland compositors (sway and friends) using the
// wlr-layer-shell protocol: a floating pill with a mic icon and live
// waveform bars, ported from the Cocoa implementation in overlay_darwin.go.
//
// Output selection: the layer surface is created with output = NULL, so the
// compositor places it on the seat's active/focused output (sway picks the
// output of the focused workspace). The surface is destroyed on every hide
// and re-created on every show, so each recording follows the cursor to
// whichever output is focused. Placement comes from the first full-size
// layer configure (all-edges anchor, exclusive zone -1), after which the
// surface switches to top+left+right anchors with a top margin of
// 0.85*H - pill_h/2, centering the pill 15% up from the bottom (wlroots
// ignores margins when both top and bottom edges are anchored, so a
// single-edge anchor is required). Buffer scale comes from
// wp_fractional-scale-v1 when the compositor provides it.
//
// A dedicated C thread owns the wl_display connection: it epoll()s on the
// Wayland socket, a command pipe, and a 20 Hz animation timerfd. The Go side
// only flips mutex-protected state and pokes the pipe, so the stt_wl_* calls
// are safe from any goroutine. Rendering goes through cairo onto wl_shm
// buffers (pango for the replay label).

#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <math.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/mman.h>
#include <sys/syscall.h>
#include <sys/timerfd.h>
#include <unistd.h>

#include <wayland-client.h>
#include <cairo.h>
#include <pango/pangocairo.h>

#include "layer_shell_v1_linux.h"
#include "fractional_scale_v1_linux.h"
#include "overlay_wayland_linux.h"

#define STT_BARS 20
#define STT_PILL_W 280.0
#define STT_PILL_H 60.0
#define STT_BAR_W 5.0
#define STT_BAR_GAP 4.0
#define STT_BAR_MAXH 40.0
#define STT_BAR_MINH 4.0
#define STT_MIC_AREA 52.0
#define STT_RIGHT_PAD 16.0
#define STT_ANIM_MS 50

// Surface lifecycle (loop-thread only).
enum {
	SURF_NONE = 0,  // no wl_surface/layer_surface objects
	SURF_WAIT_CFG,  // map requested, waiting for layer configure
	SURF_MAPPED
};

// ---------------------------------------------------------------------------
// Shared state (mutex-protected, read by the loop thread each wake-up).

static pthread_mutex_t stt_mtx = PTHREAD_MUTEX_INITIALIZER;
static int stt_alive;   // 0 once the connection is dead or init failed
static int stt_vis;
static int stt_dirty;   // pixels stale, redraw when mapped
static int stt_transcribing;
static int stt_shift;
static int stt_above;
static double stt_smoothed[STT_BARS];
static double stt_phase;
static char stt_replay[512];

struct snapshot {
	int transcribing, shift, above;
	double smoothed[STT_BARS];
	double phase;
	char replay[512];
};

// ---------------------------------------------------------------------------
// Wayland globals (loop thread, except those bound during init).

static struct wl_display *dpy;
static struct wl_registry *registry;
static struct wl_compositor *compositor;
static struct wl_shm *shm;
static struct zwlr_layer_shell_v1 *layer_shell;
static struct wp_fractional_scale_manager_v1 *frac_mgr; // optional
static int overlay_scale = 1;
static int has_argb;

static struct wl_surface *surface;
static struct zwlr_layer_surface_v1 *layer;
static struct wp_fractional_scale_v1 *frac;
static int surf_state = SURF_NONE;
static int mapped_scale;            // buffer scale applied to the surface
static int anchor_h;                // output height from the first configure

static int epfd, pipefd[2], timerfd_;
static int timer_armed;

struct frame {
	struct wl_shm_pool *pool;
	struct wl_buffer *buf;
	void *data;
	int pending;
};
static struct frame frames[3];

// ---------------------------------------------------------------------------
// Small helpers.

static void rounded_rect(cairo_t *cr, double x, double y, double w, double h, double r)
{
	if (r > w / 2) r = w / 2;
	if (r > h / 2) r = h / 2;
	cairo_new_sub_path(cr);
	cairo_arc(cr, x + w - r, y + r, r, -M_PI / 2, 0);
	cairo_arc(cr, x + w - r, y + h - r, r, 0, M_PI / 2);
	cairo_arc(cr, x + r, y + h - r, r, M_PI / 2, M_PI);
	cairo_arc(cr, x + r, y + r, r, M_PI, 3 * M_PI / 2);
	cairo_close_path(cr);
}

static void wake(void)
{
	char c = 'w';
	ssize_t ignored = write(pipefd[1], &c, 1);
	(void)ignored;
}

// ---------------------------------------------------------------------------
// Wayland listeners.

static void frame_release(void *data, struct wl_buffer *buf)
{
	struct frame *f = data;
	if (f->buf == buf) {
		wl_shm_pool_destroy(f->pool);
		f->pool = NULL;
		f->buf = NULL;
		f->data = NULL;
		f->pending = 0;
	}
}

static const struct wl_buffer_listener frame_listener = { frame_release };

static void teardown_surface(void);

static void frac_preferred_scale(void *data, struct wp_fractional_scale_v1 *fs,
                                 uint32_t scale)
{
	(void)data; (void)fs;
	int s = ((int)scale + 60) / 120; // numerator in 1/120ths, round to integer
	if (s < 1) {
		s = 1;
	}
	if (s > 4) {
		s = 4;
	}
	if (s != overlay_scale) {
		overlay_scale = s;
		pthread_mutex_lock(&stt_mtx);
		stt_dirty = 1; // re-render at the new buffer scale
		pthread_mutex_unlock(&stt_mtx);
	}
}

static const struct wp_fractional_scale_v1_listener frac_listener = {
	.preferred_scale = frac_preferred_scale,
};

static void shm_format(void *data, struct wl_shm *s, uint32_t format)
{
	(void)data; (void)s;
	if (format == WL_SHM_FORMAT_ARGB8888) {
		has_argb = 1;
	}
}

static const struct wl_shm_listener shm_listener = { shm_format };

static void layer_configure(void *data, struct zwlr_layer_surface_v1 *l,
                            uint32_t serial, uint32_t width, uint32_t height)
{
	(void)data;
	zwlr_layer_surface_v1_ack_configure(l, serial);
	// The first configure for the all-edges anchor reports the size of the
	// output the compositor picked (sway: the seat's focused output). Then
	// switch to top+left+right anchors: top-only vertical anchoring places
	// the surface at exactly margin.top (wlroots 0.18 centers top+bottom
	// anchors while ignoring the margins). Margin 0.85*H - pill_h/2 centers
	// the pill 15% up from the bottom; left+right center it horizontally.
	if (anchor_h == 0 && (width > (uint32_t)STT_PILL_W || height > (uint32_t)STT_PILL_H)) {
		anchor_h = (int)height;
		zwlr_layer_surface_v1_set_anchor(
		    layer, ZWLR_LAYER_SURFACE_V1_ANCHOR_TOP | ZWLR_LAYER_SURFACE_V1_ANCHOR_LEFT |
		           ZWLR_LAYER_SURFACE_V1_ANCHOR_RIGHT);
		zwlr_layer_surface_v1_set_size(l, (uint32_t)STT_PILL_W, (uint32_t)STT_PILL_H);
		zwlr_layer_surface_v1_set_margin(l, (int)(0.85 * anchor_h) - (int)(STT_PILL_H / 2),
		                                 0, 0, 0);
		wl_surface_commit(surface);
	}
	if (surf_state == SURF_WAIT_CFG) {
		surf_state = SURF_MAPPED;
		stt_dirty = 1;
	}
}

static void layer_closed(void *data, struct zwlr_layer_surface_v1 *l)
{
	(void)data;
	teardown_surface(); // the compositor dropped us (e.g. output gone)
	pthread_mutex_lock(&stt_mtx);
	stt_vis = 0; // stay hidden until next Show(), which picks a new output
	pthread_mutex_unlock(&stt_mtx);
}

static const struct zwlr_layer_surface_v1_listener layer_listener = {
	.configure = layer_configure,
	.closed = layer_closed,
};

static void registry_global(void *data, struct wl_registry *reg, uint32_t name,
                            const char *iface, uint32_t version)
{
	(void)data;
	if (strcmp(iface, wl_compositor_interface.name) == 0) {
		compositor = wl_registry_bind(reg, name, &wl_compositor_interface,
		                              version < 4 ? version : 4);
	} else if (strcmp(iface, wl_shm_interface.name) == 0) {
		shm = wl_registry_bind(reg, name, &wl_shm_interface, 1);
		wl_shm_add_listener(shm, &shm_listener, NULL);
	} else if (strcmp(iface, zwlr_layer_shell_v1_interface.name) == 0) {
		layer_shell = wl_registry_bind(reg, name, &zwlr_layer_shell_v1_interface,
		                               version < 3 ? version : 3);
	} else if (strcmp(iface, wp_fractional_scale_manager_v1_interface.name) == 0 &&
	           frac_mgr == NULL) {
		frac_mgr = wl_registry_bind(reg, name,
		                            &wp_fractional_scale_manager_v1_interface, 1);
	}
}

static void registry_global_remove(void *data, struct wl_registry *reg, uint32_t name)
{
	(void)data; (void)reg; (void)name;
}

static const struct wl_registry_listener registry_listener = {
	.global = registry_global,
	.global_remove = registry_global_remove,
};

// ---------------------------------------------------------------------------
// Drawing (loop thread).

static void draw_mic(cairo_t *cr, double cx, double cy, double h)
{
	double w = h * 0.36, r = w / 2;

	cairo_set_source_rgba(cr, 1, 1, 1, 1);
	rounded_rect(cr, cx - w / 2, cy - h / 2, w, h * 0.62, r);
	cairo_fill(cr);

	cairo_set_line_width(cr, h * 0.085);
	cairo_set_line_cap(cr, CAIRO_LINE_CAP_ROUND);
	cairo_new_path(cr);
	cairo_arc(cr, cx, cy - h * 0.20, h * 0.42, 0.10 * M_PI, 0.90 * M_PI);
	cairo_stroke(cr);

	cairo_move_to(cr, cx, cy - h * 0.20 + h * 0.42);
	cairo_line_to(cr, cx, cy + h * 0.5);
	cairo_stroke(cr);
}

static void draw_enter(cairo_t *cr, double x, double y, double w, double h)
{
	cairo_set_line_width(cr, 1.8);
	cairo_set_line_cap(cr, CAIRO_LINE_CAP_ROUND);
	cairo_set_line_join(cr, CAIRO_LINE_JOIN_ROUND);

	cairo_move_to(cr, x + w, y + h * 0.25);
	cairo_line_to(cr, x + w * 0.35, y + h * 0.25);
	cairo_line_to(cr, x + w * 0.35, y + h * 0.70);
	cairo_stroke(cr);

	cairo_move_to(cr, x + w * 0.62, y + h * 0.45);
	cairo_line_to(cr, x + w * 0.12, y + h * 0.70);
	cairo_line_to(cr, x + w * 0.62, y + h * 0.95);
	cairo_stroke(cr);
}

static void draw_icon(cairo_t *cr, const struct snapshot *s)
{
	double cy = STT_PILL_H / 2;

	if (s->shift) {
		cairo_save(cr);
		cairo_set_line_width(cr, 1.6);
		// mic, scaled down, plus a return-arrow beside it
		draw_mic(cr, 24.0, cy, 22.0);
		cairo_restore(cr);
		draw_enter(cr, 38.0, cy - 9.0, 14.0, 18.0);
	} else {
		draw_mic(cr, 33.0, cy, 28.0);
	}
}

static void draw_replay(cairo_t *cr, const struct snapshot *s)
{
	PangoLayout *lay = pango_cairo_create_layout(cr);
	PangoFontDescription *fd = pango_font_description_from_string("Sans 10");
	pango_layout_set_font_description(lay, fd);
	pango_font_description_free(fd);
	pango_layout_set_width(lay, (int)((STT_PILL_W - STT_MIC_AREA - STT_RIGHT_PAD) * PANGO_SCALE));
	pango_layout_set_ellipsize(lay, PANGO_ELLIPSIZE_END);
	pango_layout_set_text(lay, s->replay, -1);

	int tw, th;
	pango_layout_get_pixel_size(lay, &tw, &th);
	cairo_set_source_rgba(cr, 1, 1, 1, 1);
	cairo_move_to(cr, STT_MIC_AREA, (STT_PILL_H - th) / 2.0);
	pango_cairo_show_layout(cr, lay);
	g_object_unref(lay);
}

static void draw_bars(cairo_t *cr, const struct snapshot *s)
{
	double total_w = STT_BARS * STT_BAR_W + (STT_BARS - 1) * STT_BAR_GAP;
	double avail = STT_PILL_W - STT_MIC_AREA - STT_RIGHT_PAD;
	double start_x = STT_MIC_AREA + (avail - total_w) / 2;
	double cy = STT_PILL_H / 2;

	for (int i = 0; i < STT_BARS; i++) {
		double value;
		if (s->transcribing) {
			double wave = sin(s->phase + i * 0.3) * 0.5 + 0.5;
			value = 0.3 + wave * 0.5;
			cairo_set_source_rgba(cr, 1, 1, 1, 0.15 + wave * 0.15);
		} else {
			value = s->smoothed[i];
			if (s->above) {
				cairo_set_source_rgba(cr, 1, 1, 1, 1);
			} else {
				cairo_set_source_rgba(cr, 1, 1, 1, 0.35);
			}
		}
		double bh = STT_BAR_MINH + value * (STT_BAR_MAXH - STT_BAR_MINH);
		rounded_rect(cr, start_x + i * (STT_BAR_W + STT_BAR_GAP), cy - bh / 2,
		             STT_BAR_W, bh, STT_BAR_W / 2);
		cairo_fill(cr);
	}
}

static struct frame *free_frame(void)
{
	for (size_t i = 0; i < sizeof(frames) / sizeof(frames[0]); i++) {
		if (!frames[i].pending) {
			return &frames[i];
		}
	}
	return NULL;
}

static void render_attach(const struct snapshot *s)
{
	int scale = overlay_scale;
	if (scale < 1) scale = 1;
	if (scale > 4) scale = 4;

	int W = (int)(STT_PILL_W * scale);
	int H = (int)(STT_PILL_H * scale);
	int stride = W * 4;

	struct frame *f = free_frame();
	if (!f) {
		return; // compositor is behind; the next tick retries
	}

	size_t size = (size_t)stride * H;
	int fd = (int)syscall(__NR_memfd_create, "sttg-overlay", 0);
	if (fd < 0) {
		return;
	}
	if (ftruncate(fd, (off_t)size) < 0) {
		close(fd);
		return;
	}
	void *data = mmap(NULL, size, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
	if (data == MAP_FAILED) {
		close(fd);
		return;
	}

	cairo_surface_t *cs = cairo_image_surface_create_for_data(
	    (unsigned char *)data, CAIRO_FORMAT_ARGB32, W, H, stride);
	cairo_t *cr = cairo_create(cs);
	cairo_scale(cr, scale, scale);

	cairo_set_operator(cr, CAIRO_OPERATOR_SOURCE);
	cairo_set_source_rgba(cr, 0, 0, 0, 0);
	cairo_paint(cr);
	cairo_set_operator(cr, CAIRO_OPERATOR_OVER);

	double inset = 0.5;
	rounded_rect(cr, inset, inset, STT_PILL_W - 2 * inset, STT_PILL_H - 2 * inset,
	             STT_PILL_H / 2 - inset);
	cairo_set_source_rgba(cr, 0.05, 0.05, 0.05, 0.85);
	cairo_fill(cr);
	rounded_rect(cr, inset, inset, STT_PILL_W - 2 * inset, STT_PILL_H - 2 * inset,
	             STT_PILL_H / 2 - inset);
	cairo_set_line_width(cr, 0.5);
	cairo_set_source_rgba(cr, 1, 1, 1, 0.08);
	cairo_stroke(cr);

	draw_icon(cr, s);
	if (s->replay[0]) {
		draw_replay(cr, s);
	} else {
		draw_bars(cr, s);
	}

	cairo_surface_flush(cs);
	cairo_destroy(cr);
	cairo_surface_destroy(cs);

	f->pool = wl_shm_create_pool(shm, fd, (int32_t)size);
	close(fd);
	munmap(data, size); // the pool maps the same fd; our pixels are already written
	if (!f->pool) {
		return;
	}
	f->buf = wl_shm_pool_create_buffer(f->pool, 0, W, H, stride, WL_SHM_FORMAT_ARGB8888);
	wl_buffer_add_listener(f->buf, &frame_listener, f);

	if (mapped_scale != scale) {
		wl_surface_set_buffer_scale(surface, scale);
		mapped_scale = scale;
	}
	wl_surface_attach(surface, f->buf, 0, 0);
	wl_surface_damage_buffer(surface, 0, 0, W, H);
	wl_surface_commit(surface);
	f->pending = 1;
}

// ---------------------------------------------------------------------------
// Surface mapping (loop thread).

static void surface_create(void)
{
	surface = wl_compositor_create_surface(compositor);

	if (frac_mgr) {
		frac = wp_fractional_scale_manager_v1_get_fractional_scale(frac_mgr, surface);
		wp_fractional_scale_v1_add_listener(frac, &frac_listener, NULL);
	}

	// Fully click-through: the overlay must never eat pointer events.
	struct wl_region *region = wl_compositor_create_region(compositor);
	wl_surface_set_input_region(surface, region);

	// NULL output: let the compositor pick the active/focused one, and
	// destroy/re-create the surface on every hide/show so it follows focus.
	layer = zwlr_layer_shell_v1_get_layer_surface(
	    layer_shell, surface, NULL, ZWLR_LAYER_SHELL_V1_LAYER_OVERLAY, "sttg");
	zwlr_layer_surface_v1_set_anchor(
	    layer, ZWLR_LAYER_SURFACE_V1_ANCHOR_TOP | ZWLR_LAYER_SURFACE_V1_ANCHOR_BOTTOM |
	           ZWLR_LAYER_SURFACE_V1_ANCHOR_LEFT | ZWLR_LAYER_SURFACE_V1_ANCHOR_RIGHT);
	zwlr_layer_surface_v1_set_exclusive_zone(layer, -1);
	zwlr_layer_surface_v1_set_keyboard_interactivity(
	    layer, ZWLR_LAYER_SURFACE_V1_KEYBOARD_INTERACTIVITY_NONE);
	anchor_h = 0;
	zwlr_layer_surface_v1_add_listener(layer, &layer_listener, NULL);
	wl_surface_commit(surface);
	wl_region_destroy(region); // referenced by the commit above
	surf_state = SURF_WAIT_CFG;
}

static void teardown_surface(void)
{
	if (layer) {
		zwlr_layer_surface_v1_destroy(layer);
		layer = NULL;
	}
	if (frac) {
		wp_fractional_scale_v1_destroy(frac);
		frac = NULL;
	}
	if (surface) {
		wl_surface_destroy(surface);
		surface = NULL;
	}
	for (size_t i = 0; i < sizeof(frames) / sizeof(frames[0]); i++) {
		if (frames[i].pending) {
			wl_shm_pool_destroy(frames[i].pool);
			frames[i].pool = NULL;
			frames[i].buf = NULL;
			frames[i].data = NULL;
			frames[i].pending = 0;
		}
	}
	surf_state = SURF_NONE;
	mapped_scale = 0;
	anchor_h = 0;
}

static void set_timer(int want)
{
	if (want == timer_armed) {
		return;
	}
	timer_armed = want;
	struct itimerspec its = { 0 };
	if (want) {
		its.it_interval.tv_nsec = STT_ANIM_MS * 1000000L;
		its.it_value = its.it_interval;
	}
	timerfd_settime(timerfd_, 0, &its, NULL);
}

// ---------------------------------------------------------------------------
// Event loop thread.

static void *loop(void *arg)
{
	(void)arg;
	for (;;) {
		pthread_mutex_lock(&stt_mtx);
		if (!stt_alive) {
			pthread_mutex_unlock(&stt_mtx);
			return NULL;
		}

		int vis = stt_vis;
		int dirty = stt_dirty;

		if (vis) {
			if (surf_state == SURF_NONE && layer_shell && compositor) {
				surface_create(); // NULL output: compositor picks the focused one
			}
		} else if (surf_state != SURF_NONE) {
			teardown_surface(); // next Show() re-creates and follows focus again
			dirty = 0;
		}

		set_timer(vis && stt_transcribing);

		if (vis && surf_state == SURF_MAPPED && (dirty || stt_transcribing)) {
			struct snapshot s;
			s.transcribing = stt_transcribing;
			s.shift = stt_shift;
			s.above = stt_above;
			memcpy(s.smoothed, stt_smoothed, sizeof(s.smoothed));
			s.phase = stt_phase;
			snprintf(s.replay, sizeof(s.replay), "%s", stt_replay);
			render_attach(&s);
			stt_dirty = 0;
		}
		pthread_mutex_unlock(&stt_mtx);

		while (wl_display_prepare_read(dpy) != 0) {
			wl_display_dispatch_pending(dpy);
		}
		wl_display_flush(dpy);

		struct epoll_event evs[4];
		int n = epoll_wait(epfd, evs, 4, -1);
		if (n < 0) {
			if (errno == EINTR) {
				wl_display_cancel_read(dpy);
				continue;
			}
			wl_display_cancel_read(dpy);
			break;
		}

		int wl_ready = 0;
		int tick = 0;
		for (int i = 0; i < n; i++) {
			int fd = evs[i].data.fd;
			if (fd == wl_display_get_fd(dpy)) {
				wl_ready = 1;
			} else if (fd == timerfd_) {
				uint64_t exp;
				ssize_t ignored = read(timerfd_, &exp, sizeof(exp));
				(void)ignored;
				tick = 1;
			} else if (fd == pipefd[0]) {
				char buf[64];
				ssize_t ignored = read(pipefd[0], buf, sizeof(buf));
				(void)ignored;
			}
		}

		if (wl_ready) {
			wl_display_read_events(dpy);
			wl_display_dispatch_pending(dpy);
		} else {
			wl_display_cancel_read(dpy);
		}

		if (wl_display_get_error(dpy) != 0) {
			pthread_mutex_lock(&stt_mtx);
			stt_alive = 0;
			pthread_mutex_unlock(&stt_mtx);
			return NULL;
		}

		if (tick) {
			pthread_mutex_lock(&stt_mtx);
			if (stt_transcribing) {
				stt_phase += 0.15;
				if (stt_phase > M_PI * 2) {
					stt_phase -= M_PI * 2;
				}
				stt_dirty = 1;
			}
			pthread_mutex_unlock(&stt_mtx);
		}
	}

	pthread_mutex_lock(&stt_mtx);
	stt_alive = 0;
	pthread_mutex_unlock(&stt_mtx);
	return NULL;
}

// ---------------------------------------------------------------------------
// Init and public API.

int stt_wl_init(void)
{
	pthread_mutex_lock(&stt_mtx);
	if (stt_alive) {
		pthread_mutex_unlock(&stt_mtx);
		return 0;
	}
	pthread_mutex_unlock(&stt_mtx);

	dpy = wl_display_connect(NULL);
	if (!dpy) {
		return -1;
	}
	registry = wl_display_get_registry(dpy);
	wl_registry_add_listener(registry, &registry_listener, NULL);
	if (wl_display_roundtrip(dpy) < 0) {
		goto fail;
	}
	// The first roundtrip's sync is sent before we bind, so per-object events
	// (e.g. shm formats) land only after it returns; roundtrip once more to
	// collect them.
	if (wl_display_roundtrip(dpy) < 0) {
		goto fail;
	}
	if (!compositor || !shm || !layer_shell || !has_argb) {
		goto fail;
	}

	if (pipe2(pipefd, O_CLOEXEC | O_NONBLOCK) < 0) {
		goto fail;
	}
	timerfd_ = timerfd_create(CLOCK_MONOTONIC, TFD_CLOEXEC | TFD_NONBLOCK);
	if (timerfd_ < 0) {
		goto fail;
	}
	epfd = epoll_create1(EPOLL_CLOEXEC);
	if (epfd < 0) {
		goto fail;
	}
	struct epoll_event ev = { .events = EPOLLIN };
	ev.data.fd = wl_display_get_fd(dpy);
	epoll_ctl(epfd, EPOLL_CTL_ADD, ev.data.fd, &ev);
	ev.data.fd = pipefd[0];
	epoll_ctl(epfd, EPOLL_CTL_ADD, pipefd[0], &ev);
	ev.data.fd = timerfd_;
	epoll_ctl(epfd, EPOLL_CTL_ADD, timerfd_, &ev);

	stt_alive = 1;
	pthread_t th;
	if (pthread_create(&th, NULL, loop, NULL) != 0) {
		stt_alive = 0;
		goto fail;
	}
	pthread_detach(th);
	return 0;

fail:
	wl_display_disconnect(dpy);
	dpy = NULL;
	return -1;
}

void stt_wl_show(void)
{
	pthread_mutex_lock(&stt_mtx);
	if (!stt_alive) {
		pthread_mutex_unlock(&stt_mtx);
		return;
	}
	for (int i = 0; i < STT_BARS; i++) {
		stt_smoothed[i] = 0;
	}
	stt_transcribing = 0;
	stt_shift = 0;
	stt_above = 0;
	stt_replay[0] = '\0';
	stt_vis = 1;
	stt_dirty = 1;
	pthread_mutex_unlock(&stt_mtx);
	wake();
}

void stt_wl_hide(void)
{
	pthread_mutex_lock(&stt_mtx);
	if (!stt_alive) {
		pthread_mutex_unlock(&stt_mtx);
		return;
	}
	stt_transcribing = 0;
	stt_replay[0] = '\0';
	stt_vis = 0;
	pthread_mutex_unlock(&stt_mtx);
	wake();
}

void stt_wl_update(float *values, int n, int above)
{
	if (n > STT_BARS) {
		n = STT_BARS;
	}
	pthread_mutex_lock(&stt_mtx);
	if (!stt_alive) {
		pthread_mutex_unlock(&stt_mtx);
		return;
	}
	for (int i = 0; i < STT_BARS; i++) {
		double v = i < n ? values[i] : 0;
		stt_smoothed[i] += (v - stt_smoothed[i]) * 0.4;
	}
	if (above) {
		stt_above = 1;
	}
	stt_dirty = 1;
	pthread_mutex_unlock(&stt_mtx);
	wake();
}

void stt_wl_set_transcribing(int on)
{
	pthread_mutex_lock(&stt_mtx);
	if (!stt_alive) {
		pthread_mutex_unlock(&stt_mtx);
		return;
	}
	stt_transcribing = on;
	stt_dirty = 1;
	pthread_mutex_unlock(&stt_mtx);
	wake();
}

void stt_wl_set_shift(int held)
{
	pthread_mutex_lock(&stt_mtx);
	if (!stt_alive) {
		pthread_mutex_unlock(&stt_mtx);
		return;
	}
	if (stt_shift != (held ? 1 : 0)) {
		stt_shift = held ? 1 : 0;
		stt_dirty = 1;
		wake();
	}
	pthread_mutex_unlock(&stt_mtx);
}

void stt_wl_set_replay(const char *utf8)
{
	pthread_mutex_lock(&stt_mtx);
	if (!stt_alive) {
		pthread_mutex_unlock(&stt_mtx);
		return;
	}
	if (utf8 != NULL && utf8[0] != '\0') {
		snprintf(stt_replay, sizeof(stt_replay), "%s", utf8);
		stt_transcribing = 0;
	} else {
		stt_replay[0] = '\0';
	}
	stt_dirty = 1;
	pthread_mutex_unlock(&stt_mtx);
	wake();
}
