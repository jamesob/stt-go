//go:build darwin

package main

/*
#cgo LDFLAGS: -framework AudioToolbox -framework CoreAudio -framework CoreFoundation
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <AudioToolbox/AudioToolbox.h>
#include <CoreAudio/CoreAudio.h>

extern void goAudioSamples(int16_t* data, int n);

#define STT_NUM_BUFFERS 3

static AudioQueueRef stt_queue = NULL;
static volatile int stt_audio_running = 0;

static void stt_input_callback(void *user, AudioQueueRef q, AudioQueueBufferRef buf,
                               const AudioTimeStamp *ts, UInt32 npackets,
                               const AudioStreamPacketDescription *descs) {
	if (stt_audio_running && buf->mAudioDataByteSize > 0) {
		goAudioSamples((int16_t *)buf->mAudioData, (int)(buf->mAudioDataByteSize / 2));
	}
	if (stt_audio_running) {
		AudioQueueEnqueueBuffer(q, buf, 0, NULL);
	}
}

// stt_copy_device_name fills buf with a device's UTF-8 display name.
// Returns 1 on success.
static int stt_copy_device_name(AudioDeviceID dev, char *buf, size_t buflen) {
	AudioObjectPropertyAddress naddr = {
		kAudioObjectPropertyName,
		kAudioObjectPropertyScopeGlobal,
		kAudioObjectPropertyElementMain
	};
	CFStringRef cfname = NULL;
	UInt32 nsize = sizeof(cfname);
	if (AudioObjectGetPropertyData(dev, &naddr, 0, NULL, &nsize, &cfname) != 0 || cfname == NULL) {
		return 0;
	}
	Boolean ok = CFStringGetCString(cfname, buf, buflen, kCFStringEncodingUTF8);
	CFRelease(cfname);
	return ok ? 1 : 0;
}

// stt_device_has_input reports whether a device has input streams.
static int stt_device_has_input(AudioDeviceID dev) {
	AudioObjectPropertyAddress saddr = {
		kAudioDevicePropertyStreams,
		kAudioObjectPropertyScopeInput,
		kAudioObjectPropertyElementMain
	};
	UInt32 ssize = 0;
	return AudioObjectGetPropertyDataSize(dev, &saddr, 0, NULL, &ssize) == 0 && ssize > 0;
}

// stt_copy_all_devices returns a malloc'd array of all device ids (caller
// frees) and stores the count.
static AudioDeviceID *stt_copy_all_devices(int *count) {
	*count = 0;
	AudioObjectPropertyAddress addr = {
		kAudioHardwarePropertyDevices,
		kAudioObjectPropertyScopeGlobal,
		kAudioObjectPropertyElementMain
	};
	UInt32 size = 0;
	if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &addr, 0, NULL, &size) != 0 || size == 0) {
		return NULL;
	}
	AudioDeviceID *devs = (AudioDeviceID *)malloc(size);
	if (devs == NULL) {
		return NULL;
	}
	if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &addr, 0, NULL, &size, devs) != 0) {
		free(devs);
		return NULL;
	}
	*count = (int)(size / sizeof(AudioDeviceID));
	return devs;
}

// stt_find_device_uid looks up an input device by its display name (as shown
// in System Settings -> Sound) and returns its UID, or NULL when not found.
static CFStringRef stt_find_device_uid(const char *name) {
	int count = 0;
	AudioDeviceID *devs = stt_copy_all_devices(&count);
	if (devs == NULL) {
		return NULL;
	}
	CFStringRef uid = NULL;
	for (int i = 0; i < count && uid == NULL; i++) {
		if (!stt_device_has_input(devs[i])) {
			continue;
		}
		char buf[256];
		if (!stt_copy_device_name(devs[i], buf, sizeof(buf))) {
			continue;
		}
		if (strcmp(buf, name) == 0) {
			AudioObjectPropertyAddress uaddr = {
				kAudioDevicePropertyDeviceUID,
				kAudioObjectPropertyScopeGlobal,
				kAudioObjectPropertyElementMain
			};
			UInt32 usize = sizeof(uid);
			if (AudioObjectGetPropertyData(devs[i], &uaddr, 0, NULL, &usize, &uid) != 0) {
				uid = NULL;
			}
		}
	}
	free(devs);
	return uid;
}

// stt_copy_input_device_names returns a malloc'd newline-separated list of
// input device names (caller frees; "" when none).
static char *stt_copy_input_device_names(void) {
	int count = 0;
	AudioDeviceID *devs = stt_copy_all_devices(&count);
	if (devs == NULL) {
		return strdup("");
	}
	size_t cap = 4096;
	size_t len = 0;
	char *out = (char *)malloc(cap);
	if (out == NULL) {
		free(devs);
		return strdup("");
	}
	out[0] = '\0';
	for (int i = 0; i < count; i++) {
		if (!stt_device_has_input(devs[i])) {
			continue;
		}
		char buf[256];
		if (!stt_copy_device_name(devs[i], buf, sizeof(buf))) {
			continue;
		}
		size_t need = strlen(buf) + 2;
		if (len + need > cap) {
			cap = (len + need) * 2;
			char *grown = (char *)realloc(out, cap);
			if (grown == NULL) {
				break;
			}
			out = grown;
		}
		if (len > 0) {
			out[len++] = '\n';
		}
		memcpy(out + len, buf, strlen(buf) + 1);
		len += strlen(buf);
	}
	free(devs);
	return out;
}

// stt_copy_default_input_device_name returns the default input device's name
// (malloc'd; caller frees; "" when unknown).
static char *stt_copy_default_input_device_name(void) {
	AudioObjectPropertyAddress addr = {
		kAudioHardwarePropertyDefaultInputDevice,
		kAudioObjectPropertyScopeGlobal,
		kAudioObjectPropertyElementMain
	};
	AudioDeviceID dev = 0;
	UInt32 size = sizeof(dev);
	if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &addr, 0, NULL, &size, &dev) != 0 || dev == 0) {
		return strdup("");
	}
	char buf[256];
	if (!stt_copy_device_name(dev, buf, sizeof(buf))) {
		return strdup("");
	}
	return strdup(buf);
}

// stt_audio_start begins capturing 16-bit mono PCM. device_name may be NULL
// or "" for the system default input. On success returns 0 and sets
// *device_missing to 1 when the named device was not found (capture then uses
// the default device). Otherwise returns the OSStatus error code.
static int stt_audio_start(int sample_rate, const char *device_name, int *device_missing) {
	if (device_missing != NULL) {
		*device_missing = 0;
	}
	if (stt_audio_running) {
		return -1;
	}
	AudioStreamBasicDescription fmt;
	memset(&fmt, 0, sizeof(fmt));
	fmt.mSampleRate = (Float64)sample_rate;
	fmt.mFormatID = kAudioFormatLinearPCM;
	fmt.mFormatFlags = kLinearPCMFormatFlagIsSignedInteger | kLinearPCMFormatFlagIsPacked;
	fmt.mBitsPerChannel = 16;
	fmt.mChannelsPerFrame = 1;
	fmt.mBytesPerFrame = 2;
	fmt.mFramesPerPacket = 1;
	fmt.mBytesPerPacket = 2;

	OSStatus st = AudioQueueNewInput(&fmt, stt_input_callback, NULL, NULL, NULL, 0, &stt_queue);
	if (st != 0) {
		stt_queue = NULL;
		return (int)st;
	}

	if (device_name != NULL && device_name[0] != '\0') {
		CFStringRef uid = stt_find_device_uid(device_name);
		if (uid != NULL) {
			// Best-effort: on failure the default device is used.
			AudioQueueSetProperty(stt_queue, kAudioQueueProperty_CurrentDevice, &uid, sizeof(uid));
			CFRelease(uid);
		} else if (device_missing != NULL) {
			*device_missing = 1;
		}
	}

	UInt32 bufbytes = (UInt32)(sample_rate / 10) * 2; // ~100ms per buffer
	for (int i = 0; i < STT_NUM_BUFFERS; i++) {
		AudioQueueBufferRef b = NULL;
		st = AudioQueueAllocateBuffer(stt_queue, bufbytes, &b);
		if (st != 0) {
			AudioQueueDispose(stt_queue, true);
			stt_queue = NULL;
			return (int)st;
		}
		AudioQueueEnqueueBuffer(stt_queue, b, 0, NULL);
	}
	stt_audio_running = 1;
	st = AudioQueueStart(stt_queue, NULL);
	if (st != 0) {
		stt_audio_running = 0;
		AudioQueueDispose(stt_queue, true);
		stt_queue = NULL;
		return (int)st;
	}
	return 0;
}

// stt_audio_stop stops the capture. AudioQueueDispose(.., true) is
// synchronous, so no callbacks run after it returns. Safe to call anytime.
static void stt_audio_stop(void) {
	if (stt_queue == NULL) {
		return;
	}
	stt_audio_running = 0;
	AudioQueueStop(stt_queue, true);
	AudioQueueDispose(stt_queue, true);
	stt_queue = NULL;
}
*/
import "C"

import (
	"fmt"
	"strings"
	"sync"
	"unsafe"
)

// darwinRecorder captures audio via AudioQueue. A single shared instance is
// used because the C callback has no per-instance context.
type darwinRecorder struct {
	mu        sync.Mutex
	active    bool
	samples   []int16
	wfSamples []int16 // samples since the last overlay poll
}

var sharedRecorder = &darwinRecorder{}

// audioCallMu serializes calls into the C audio layer.
var audioCallMu sync.Mutex

func (r *darwinRecorder) append(s []int16) {
	r.mu.Lock()
	if r.active {
		r.samples = append(r.samples, s...)
		r.wfSamples = append(r.wfSamples, s...)
	}
	r.mu.Unlock()
}

// TakeWaveformSamples implements WaveformSource for the overlay.
func (r *darwinRecorder) TakeWaveformSamples() []int16 {
	r.mu.Lock()
	s := r.wfSamples
	r.wfSamples = nil
	r.mu.Unlock()
	return s
}

func (r *darwinRecorder) Start(deviceName string) error {
	r.mu.Lock()
	if r.active {
		r.mu.Unlock()
		return fmt.Errorf("already recording")
	}
	r.samples = nil
	r.wfSamples = nil
	r.active = true
	r.mu.Unlock()

	cDev := C.CString(deviceName)
	defer C.free(unsafe.Pointer(cDev))
	var missing C.int

	audioCallMu.Lock()
	rc := int(C.stt_audio_start(C.int(sampleRate), cDev, &missing))
	audioCallMu.Unlock()

	if rc != 0 {
		r.mu.Lock()
		r.active = false
		r.mu.Unlock()
		return fmt.Errorf("AudioQueue start failed (OSStatus %d)", rc)
	}
	if missing != 0 {
		warn("Audio device %q not found; using system default", deviceName)
	}
	return nil
}

func (r *darwinRecorder) Stop() ([]int16, error) {
	audioCallMu.Lock()
	C.stt_audio_stop()
	audioCallMu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return nil, nil
	}
	r.active = false
	s := r.samples
	r.samples = nil
	r.wfSamples = nil
	return s, nil
}

func (r *darwinRecorder) Cancel() {
	audioCallMu.Lock()
	C.stt_audio_stop()
	audioCallMu.Unlock()

	r.mu.Lock()
	r.active = false
	r.samples = nil
	r.wfSamples = nil
	r.mu.Unlock()
}

// platformInputDevices lists input device names and the default input device
// for the setup wizard.
func platformInputDevices() ([]string, string) {
	audioCallMu.Lock()
	cnames := C.stt_copy_input_device_names()
	cdef := C.stt_copy_default_input_device_name()
	audioCallMu.Unlock()
	defer C.free(unsafe.Pointer(cnames))
	defer C.free(unsafe.Pointer(cdef))

	var names []string
	for _, n := range strings.Split(C.GoString(cnames), "\n") {
		if n != "" {
			names = append(names, n)
		}
	}
	return names, C.GoString(cdef)
}
