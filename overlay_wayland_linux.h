//go:build linux

#ifndef OVERLAY_WAYLAND_LINUX_H
#define OVERLAY_WAYLAND_LINUX_H

// Thread-safe Wayland layer-shell overlay for the recording pill. Mirrors the
// Cocoa API in overlay_darwin.go: init connects to the compositor and starts
// an event-loop thread; the stt_wl_* functions may be called from any thread.
// If there is no usable Wayland session, stt_wl_init returns -1 and every
// other call is a no-op.

int stt_wl_init(void);
void stt_wl_show(void);
void stt_wl_hide(void);
void stt_wl_update(float *values, int n, int above);
void stt_wl_set_transcribing(int on);
void stt_wl_set_shift(int held);
void stt_wl_set_replay(const char *utf8);

#endif
