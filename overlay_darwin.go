//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#include <dispatch/dispatch.h>
#include <math.h>
#include <stdlib.h>
#include <string.h>

// Recording overlay: a floating pill with a mic icon and live waveform bars,
// a straight port of the Python version's overlay.py. All UI work happens on
// the main queue; the public stt_overlay_* functions are thread-safe.

#define STT_BARS 20
#define STT_PILL_W 280.0
#define STT_PILL_H 60.0
#define STT_BAR_W 5.0
#define STT_BAR_GAP 4.0
#define STT_BAR_MAXH 40.0
#define STT_BAR_MINH 4.0
#define STT_MIC_AREA 52.0
#define STT_RIGHT_PAD 16.0

@interface STTOverlayView : NSView {
@public
	double bars[STT_BARS];
	double smoothed[STT_BARS];
	BOOL thresholdCrossed;
	BOOL transcribing;
	BOOL shiftHeld;
	double phase;
	NSString *replayText;
}
@end

@implementation STTOverlayView

- (void)drawMicIcon:(NSRect)bounds {
	NSImageSymbolConfiguration *sizeConf =
	    [NSImageSymbolConfiguration configurationWithPointSize:28 weight:NSFontWeightRegular];
	NSImageSymbolConfiguration *colorConf =
	    [NSImageSymbolConfiguration configurationWithHierarchicalColor:[NSColor whiteColor]];
	NSImageSymbolConfiguration *combined = [sizeConf configurationByApplyingConfiguration:colorConf];

	NSImage *mic = [NSImage imageWithSystemSymbolName:@"mic" accessibilityDescription:@"Recording"];
	if (mic == nil) {
		return;
	}
	mic = [mic imageWithSymbolConfiguration:combined];
	if (mic == nil) {
		return;
	}

	CGFloat centerY = bounds.size.height / 2;

	if (self->shiftHeld) {
		NSImage *enter = [NSImage imageWithSystemSymbolName:@"return" accessibilityDescription:@"Enter"];
		if (enter != nil) {
			enter = [enter imageWithSymbolConfiguration:combined];
		}

		NSSize micSize = [mic size];
		CGFloat micScale = 22.0 / MAX(micSize.width, micSize.height);
		CGFloat micW = micSize.width * micScale;
		CGFloat micH = micSize.height * micScale;

		CGFloat enterW = 0, enterH = 0;
		if (enter != nil) {
			NSSize es = [enter size];
			CGFloat escale = 18.0 / MAX(es.width, es.height);
			enterW = es.width * escale;
			enterH = es.height * escale;
		}

		CGFloat gap = 4;
		CGFloat totalW = micW + gap + enterW;
		CGFloat startX = (STT_MIC_AREA - totalW) / 2 + 12;

		[mic drawInRect:NSMakeRect(startX, centerY - micH / 2, micW, micH)];
		if (enter != nil) {
			[enter drawInRect:NSMakeRect(startX + micW + gap, centerY - enterH / 2, enterW, enterH)];
		}
	} else {
		NSSize imgSize = [mic size];
		CGFloat scale = 28.0 / MAX(imgSize.width, imgSize.height);
		CGFloat w = imgSize.width * scale;
		CGFloat h = imgSize.height * scale;
		CGFloat centerX = 14 + (STT_MIC_AREA - 14) / 2;
		[mic drawInRect:NSMakeRect(centerX - w / 2, centerY - h / 2, w, h)];
	}
}

- (void)drawRect:(NSRect)dirtyRect {
	NSRect bounds = [self bounds];
	CGFloat inset = 0.5;
	NSRect pillRect = NSMakeRect(bounds.origin.x + inset, bounds.origin.y + inset,
	                             bounds.size.width - 2 * inset, bounds.size.height - 2 * inset);
	CGFloat radius = bounds.size.height / 2 - inset;

	NSBezierPath *pill = [NSBezierPath bezierPathWithRoundedRect:pillRect xRadius:radius yRadius:radius];
	[[NSColor colorWithCalibratedRed:0.05 green:0.05 blue:0.05 alpha:0.85] setFill];
	[pill fill];
	[[NSColor colorWithCalibratedRed:1 green:1 blue:1 alpha:0.08] setStroke];
	[pill setLineWidth:0.5];
	[pill stroke];

	[self drawMicIcon:bounds];

	if (self->replayText != nil) {
		NSDictionary *attrs = @{
			NSFontAttributeName : [NSFont systemFontOfSize:13 weight:NSFontWeightMedium],
			NSForegroundColorAttributeName : [NSColor whiteColor],
		};
		NSSize ts = [self->replayText sizeWithAttributes:attrs];
		[self->replayText drawAtPoint:NSMakePoint(STT_MIC_AREA, bounds.size.height / 2 - ts.height / 2)
		               withAttributes:attrs];
		return;
	}

	CGFloat totalW = STT_BARS * STT_BAR_W + (STT_BARS - 1) * STT_BAR_GAP;
	CGFloat avail = bounds.size.width - STT_MIC_AREA - STT_RIGHT_PAD;
	CGFloat startX = STT_MIC_AREA + (avail - totalW) / 2;
	CGFloat centerY = bounds.size.height / 2;

	if (!self->transcribing) {
		NSColor *barColor = self->thresholdCrossed
		    ? [NSColor whiteColor]
		    : [NSColor colorWithCalibratedRed:1 green:1 blue:1 alpha:0.35];
		[barColor setFill];
	}

	for (int i = 0; i < STT_BARS; i++) {
		double value;
		if (self->transcribing) {
			double wave = sin(self->phase + i * 0.3) * 0.5 + 0.5;
			value = 0.3 + wave * 0.5;
			[[NSColor colorWithCalibratedRed:1 green:1 blue:1 alpha:0.15 + wave * 0.15] setFill];
		} else {
			value = self->smoothed[i];
		}
		CGFloat bh = STT_BAR_MINH + value * (STT_BAR_MAXH - STT_BAR_MINH);
		NSRect r = NSMakeRect(startX + i * (STT_BAR_W + STT_BAR_GAP), centerY - bh / 2, STT_BAR_W, bh);
		[[NSBezierPath bezierPathWithRoundedRect:r xRadius:STT_BAR_W / 2 yRadius:STT_BAR_W / 2] fill];
	}
}
@end

static NSWindow *stt_panel = nil;
static STTOverlayView *stt_view = nil;
static NSTimer *stt_anim_timer = nil;

// Main-thread-only helpers.

static void stt_overlay_ensure(void) {
	if (stt_panel != nil) {
		return;
	}
	NSRect frame = NSMakeRect(0, 0, STT_PILL_W, STT_PILL_H);
	stt_panel = [[NSWindow alloc] initWithContentRect:frame
	                                        styleMask:NSWindowStyleMaskBorderless
	                                          backing:NSBackingStoreBuffered
	                                            defer:NO];
	[stt_panel setLevel:NSFloatingWindowLevel];
	[stt_panel setOpaque:NO];
	[stt_panel setBackgroundColor:[NSColor clearColor]];
	[stt_panel setIgnoresMouseEvents:YES];
	[stt_panel setHasShadow:NO];
	[stt_panel setReleasedWhenClosed:NO];
	[stt_panel setCollectionBehavior:(NSWindowCollectionBehaviorCanJoinAllSpaces |
	                                  NSWindowCollectionBehaviorFullScreenAuxiliary)];
	stt_view = [[STTOverlayView alloc] initWithFrame:frame];
	[stt_panel setContentView:stt_view];
}

// Position on the screen containing the mouse cursor, centered horizontally,
// one third from the bottom (same as the Python version).
static void stt_overlay_position(void) {
	NSPoint mouse = [NSEvent mouseLocation];
	NSScreen *screen = nil;
	for (NSScreen *s in [NSScreen screens]) {
		if (NSPointInRect(mouse, [s frame])) {
			screen = s;
			break;
		}
	}
	if (screen == nil) {
		screen = [NSScreen mainScreen];
	}
	if (screen == nil) {
		return;
	}
	NSRect f = [screen frame];
	CGFloat x = f.origin.x + (f.size.width - STT_PILL_W) / 2;
	CGFloat y = f.origin.y + f.size.height / 3 - STT_PILL_H / 2;
	[stt_panel setFrameOrigin:NSMakePoint(x, y)];
}

static void stt_set_transcribing_main(int on) {
	stt_overlay_ensure();
	stt_view->transcribing = on ? YES : NO;
	if (on) {
		stt_view->replayText = nil;
	}
	if (on && stt_anim_timer == nil) {
		stt_anim_timer = [NSTimer scheduledTimerWithTimeInterval:0.05
		                                                 repeats:YES
		                                                   block:^(NSTimer *t) {
			stt_view->phase += 0.15;
			if (stt_view->phase > M_PI * 2) {
				stt_view->phase -= M_PI * 2;
			}
			[stt_view setNeedsDisplay:YES];
		}];
	} else if (!on && stt_anim_timer != nil) {
		[stt_anim_timer invalidate];
		stt_anim_timer = nil;
	}
	[stt_view setNeedsDisplay:YES];
}

// Public, thread-safe API (dispatches to the main queue).

static void stt_overlay_show(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		stt_overlay_ensure();
		for (int i = 0; i < STT_BARS; i++) {
			stt_view->bars[i] = 0;
			stt_view->smoothed[i] = 0;
		}
		stt_view->shiftHeld = NO;
		stt_view->thresholdCrossed = NO;
		stt_view->replayText = nil;
		stt_set_transcribing_main(0);
		stt_overlay_position();
		[stt_panel orderFrontRegardless];
	});
}

static void stt_overlay_hide(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (stt_panel == nil) {
			return;
		}
		stt_set_transcribing_main(0);
		stt_view->replayText = nil;
		[stt_panel orderOut:nil];
	});
}

static void stt_overlay_update(float *values, int n, int above) {
	if (n > STT_BARS) {
		n = STT_BARS;
	}
	float *copy = (float *)calloc(STT_BARS, sizeof(float));
	if (copy == NULL) {
		return;
	}
	if (n > 0) {
		memcpy(copy, values, sizeof(float) * n);
	}
	dispatch_async(dispatch_get_main_queue(), ^{
		stt_overlay_ensure();
		for (int i = 0; i < STT_BARS; i++) {
			stt_view->bars[i] = copy[i];
			stt_view->smoothed[i] += (stt_view->bars[i] - stt_view->smoothed[i]) * 0.4;
		}
		if (above) {
			stt_view->thresholdCrossed = YES;
		}
		[stt_view setNeedsDisplay:YES];
		free(copy);
	});
}

static void stt_overlay_set_transcribing(int on) {
	dispatch_async(dispatch_get_main_queue(), ^{
		stt_set_transcribing_main(on);
	});
}

static void stt_overlay_set_shift(int held) {
	dispatch_async(dispatch_get_main_queue(), ^{
		stt_overlay_ensure();
		if (stt_view->shiftHeld != (held ? YES : NO)) {
			stt_view->shiftHeld = held ? YES : NO;
			[stt_view setNeedsDisplay:YES];
		}
	});
}

static void stt_overlay_set_replay(const char *utf8) {
	char *copy = NULL;
	if (utf8 != NULL && utf8[0] != '\0') {
		copy = strdup(utf8);
	}
	dispatch_async(dispatch_get_main_queue(), ^{
		stt_overlay_ensure();
		if (copy == NULL) {
			stt_view->replayText = nil;
		} else {
			stt_set_transcribing_main(0);
			stt_view->replayText = [NSString stringWithUTF8String:copy];
			free(copy);
		}
		[stt_view setNeedsDisplay:YES];
	});
}

// stt_overlay_runloop runs the Cocoa main loop. Must be called on the main
// thread; never returns.
static void stt_overlay_runloop(void) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
	[NSApp run];
}
*/
import "C"

import (
	"runtime"
	"unsafe"
)

func init() {
	// The Cocoa main loop must run on the process's main thread.
	runtime.LockOSThread()
}

// darwinOverlay renders the recording pill via Cocoa. All methods are safe to
// call from any goroutine.
type darwinOverlay struct{}

func (darwinOverlay) Show() { C.stt_overlay_show() }
func (darwinOverlay) Hide() { C.stt_overlay_hide() }

func (darwinOverlay) UpdateWaveform(values []float64, aboveThreshold bool) {
	if len(values) == 0 {
		return
	}
	arr := make([]C.float, len(values))
	for i, v := range values {
		arr[i] = C.float(v)
	}
	C.stt_overlay_update(&arr[0], C.int(len(arr)), cBool(aboveThreshold))
}

func (darwinOverlay) SetTranscribing(on bool) { C.stt_overlay_set_transcribing(cBool(on)) }
func (darwinOverlay) SetShiftHeld(held bool)  { C.stt_overlay_set_shift(cBool(held)) }

func (darwinOverlay) SetReplay(text string) {
	cs := C.CString(text)
	defer C.free(unsafe.Pointer(cs))
	C.stt_overlay_set_replay(cs)
}

func cBool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// runMainLoop runs the Cocoa main loop; required for the overlay. Never
// returns (the app exits via the signal handler).
func runMainLoop() {
	C.stt_overlay_runloop()
}
