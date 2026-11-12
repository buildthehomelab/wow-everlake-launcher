//go:build darwin && cgo

package appwindow

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

// Stops [NSApp run] so control returns to Go. terminate: would exit the process from inside Cocoa instead.
static void elStop(void) {
	[NSApp stop:nil];
	NSEvent *e = [NSEvent otherEventWithType:NSEventTypeApplicationDefined location:NSZeroPoint modifierFlags:0
		timestamp:0 windowNumber:0 context:nil subtype:0 data1:0 data2:0];
	[NSApp postEvent:e atStart:YES];
}

@interface ELDelegate : NSObject <NSApplicationDelegate, NSWindowDelegate>
@end

@implementation ELDelegate
- (void)windowWillClose:(NSNotification *)n { elStop(); }
// Cmd-Q and Quit from the Dock end the run loop the same way closing the window does.
- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)app {
	elStop();
	return NSTerminateCancel;
}
@end

// The title bar is see-through and the page runs up under it (only the three window buttons sit over it), so the
// window reads as one dark surface. The page covers the strip where the title bar was, so this invisible view on top
// of it keeps the window draggable there; double-click zooms, as a title bar does.
@interface ELDragStrip : NSView
@end

@implementation ELDragStrip
- (BOOL)mouseDownCanMoveWindow { return YES; }
- (void)mouseDown:(NSEvent *)e {
	if (e.clickCount == 2) {
		[self.window performZoom:nil];
		return;
	}
	[self.window performWindowDragWithEvent:e];
}
@end

static ELDelegate *elDelegate;
static NSWindow *elWindow;

static void elApp(void) {
	[NSApplication sharedApplication];
	// A bare binary run from a terminal is a background process unless told otherwise: no Dock icon, no menu bar,
	// and its window can't take keyboard focus.
	[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
}

static NSMenuItem *elItem(NSString *title, SEL action, NSString *key) {
	return [[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key];
}

// The menu bar: Quit, and an Edit menu, without which Cmd-C/Cmd-V/Cmd-A don't reach the page's text fields.
static void elMenu(NSString *appName) {
	NSMenu *bar = [NSMenu new];
	NSMenuItem *appItem = [NSMenuItem new];
	NSMenu *app = [NSMenu new];
	[app addItem:elItem([@"Hide " stringByAppendingString:appName], @selector(hide:), @"h")];
	[app addItem:[NSMenuItem separatorItem]];
	[app addItem:elItem([@"Quit " stringByAppendingString:appName], @selector(terminate:), @"q")];
	appItem.submenu = app;
	[bar addItem:appItem];

	NSMenuItem *editItem = [NSMenuItem new];
	NSMenu *edit = [[NSMenu alloc] initWithTitle:@"Edit"];
	[edit addItem:elItem(@"Undo", @selector(undo:), @"z")];
	NSMenuItem *redo = elItem(@"Redo", @selector(redo:), @"z");
	redo.keyEquivalentModifierMask = NSEventModifierFlagCommand | NSEventModifierFlagShift;
	[edit addItem:redo];
	[edit addItem:[NSMenuItem separatorItem]];
	[edit addItem:elItem(@"Cut", @selector(cut:), @"x")];
	[edit addItem:elItem(@"Copy", @selector(copy:), @"c")];
	[edit addItem:elItem(@"Paste", @selector(paste:), @"v")];
	[edit addItem:elItem(@"Select All", @selector(selectAll:), @"a")];
	editItem.submenu = edit;
	[bar addItem:editItem];

	NSMenuItem *windowItem = [NSMenuItem new];
	NSMenu *window = [[NSMenu alloc] initWithTitle:@"Window"];
	[window addItem:elItem(@"Minimize", @selector(performMiniaturize:), @"m")];
	[window addItem:elItem(@"Close", @selector(performClose:), @"w")];
	windowItem.submenu = window;
	[bar addItem:windowItem];
	NSApp.mainMenu = bar;
	NSApp.windowsMenu = window;
}

static void elRun(const char *title, const char *url, int width, int height) {
	@autoreleasepool {
		elApp();
		NSString *name = [NSString stringWithUTF8String:title];
		elDelegate = [ELDelegate new];
		NSApp.delegate = elDelegate;
		elMenu(name);

		NSRect frame = NSMakeRect(0, 0, width, height);
		elWindow = [[NSWindow alloc] initWithContentRect:frame
			styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable |
				NSWindowStyleMaskResizable | NSWindowStyleMaskFullSizeContentView
			backing:NSBackingStoreBuffered defer:NO];
		elWindow.releasedWhenClosed = NO;
		elWindow.title = name;
		elWindow.delegate = elDelegate;
		elWindow.titlebarAppearsTransparent = YES;
		elWindow.titleVisibility = NSWindowTitleHidden;
		elWindow.appearance = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
		// Shows until the page's first paint, instead of a white flash; close to every colour the page uses.
		elWindow.backgroundColor = [NSColor colorWithSRGBRed:0x0e/255.0 green:0x0d/255.0 blue:0x18/255.0 alpha:1];
		elWindow.minSize = NSMakeSize(480, 420);
		[elWindow center];
		[elWindow setFrameAutosaveName:@"EverlakeLauncherCompact"]; // reopens where the player left it

		NSView *root = [[NSView alloc] initWithFrame:frame];
		WKWebView *web = [[WKWebView alloc] initWithFrame:frame configuration:[WKWebViewConfiguration new]];
		web.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
		[web setValue:@NO forKey:@"drawsBackground"]; // the window's colour shows until the page paints
		[root addSubview:web];
		CGFloat strip = 28; // the title bar's height
		ELDragStrip *drag = [[ELDragStrip alloc] initWithFrame:NSMakeRect(0, height - strip, width, strip)];
		drag.autoresizingMask = NSViewWidthSizable | NSViewMinYMargin;
		[root addSubview:drag];
		elWindow.contentView = root;
		[web loadRequest:[NSURLRequest requestWithURL:[NSURL URLWithString:[NSString stringWithUTF8String:url]]]];

		[elWindow makeKeyAndOrderFront:nil];
		[NSApp activateIgnoringOtherApps:YES];
		[NSApp run];

		elWindow.delegate = nil;
		[elWindow orderOut:nil];
		elWindow = nil;
	}
}

static void elStopAsync(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ elStop(); });
}

static void elAlert(const char *title, const char *msg) {
	@autoreleasepool {
		elApp();
		NSAlert *a = [NSAlert new];
		a.messageText = [NSString stringWithUTF8String:title];
		a.informativeText = [NSString stringWithUTF8String:msg];
		[NSApp activateIgnoringOtherApps:YES];
		[a runModal];
	}
}
*/
import "C"

import (
	"unsafe"
)

func run(o Options) error {
	title, url := C.CString(o.Title), C.CString(o.URL)
	defer C.free(unsafe.Pointer(title))
	defer C.free(unsafe.Pointer(url))
	done := make(chan struct{})
	defer close(done)
	if o.Stop != nil {
		go func() {
			select {
			case <-o.Stop:
				C.elStopAsync()
			case <-done:
			}
		}()
	}
	C.elRun(title, url, C.int(o.Width), C.int(o.Height))
	return nil
}

func alert(title, msg string) bool {
	t, m := C.CString(title), C.CString(msg)
	defer C.free(unsafe.Pointer(t))
	defer C.free(unsafe.Pointer(m))
	C.elAlert(t, m)
	return true
}
