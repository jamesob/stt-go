//go:build darwin

package main

/*
#include <stdint.h>
*/
import "C"

import "unsafe"

// Cgo exports called from the C layers in hotkey_darwin.go and
// audio_darwin.go. They live in their own file because a file with //export
// directives may only declare (not define) C code in its preamble.

//export goKeyEvent
func goKeyEvent(t C.int, keycode C.int64_t, flags C.uint64_t) C.int {
	if l := activeListener.Load(); l != nil {
		if l.handleKeyEvent(int(t), int64(keycode), uint64(flags)) {
			return 1
		}
	}
	return 0
}

//export goAudioSamples
func goAudioSamples(data *C.int16_t, n C.int) {
	if data == nil || n <= 0 {
		return
	}
	samples := unsafe.Slice((*int16)(unsafe.Pointer(data)), int(n))
	sharedRecorder.append(samples)
}
