package ui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

static void setDockIconVisible(int visible) {
	[NSApp setActivationPolicy:visible ? NSApplicationActivationPolicyRegular : NSApplicationActivationPolicyAccessory];
	[NSApp activateIgnoringOtherApps:YES];
}
*/
import "C"

const dockIconSupported = true

// setDockIconVisible must run on the main (UI) thread.
func setDockIconVisible(visible bool) {
	v := C.int(0)
	if visible {
		v = 1
	}
	C.setDockIconVisible(v)
}
