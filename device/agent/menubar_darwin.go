//go:build darwin && cgo

package main

// The menu bar item (#159): the way back to the settings page on a Mac, for someone who
// does not use a terminal. Linux has its applications menu entry instead, and Windows its
// notification area icon (#160). The item does what `agent settings` does, so nothing in
// it is needed to configure a PC.
//
// A status item is AppKit, and AppKit is reached from C. The definitions sit here and the
// two callbacks in menubar_export_darwin.go, because cgo copies the preamble of a file
// with //export into two C files and a definition there is then defined twice.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

void menuOpenSettings(void);
void menuQuit(void);

@interface MenuTarget : NSObject
@end

@implementation MenuTarget
- (void)open:(id)sender { menuOpenSettings(); }
- (void)quit:(id)sender { menuQuit(); }
@end

static void runMenuBar(void) {
	@autoreleasepool {
		[NSApplication sharedApplication];
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
		NSStatusItem *item = [[NSStatusBar systemStatusBar] statusItemWithLength:NSSquareStatusItemLength];
		item.button.image = [NSImage imageWithSystemSymbolName:@"laptopcomputer" accessibilityDescription:@"anywhere-file"];
		MenuTarget *target = [MenuTarget new];
		NSMenu *menu = [NSMenu new];
		[menu addItemWithTitle:@"Open settings" action:@selector(open:) keyEquivalent:@""].target = target;
		[menu addItemWithTitle:@"Quit" action:@selector(quit:) keyEquivalent:@""].target = target;
		item.menu = menu;
		// Never returns, so item and target live as long as the process.
		[NSApp run];
	}
}
*/
import "C"

import (
	"context"
	"log/slog"
	"os"
	"runtime"
)

// AppKit runs on the process's first thread or not at all, and only the main goroutine
// can be kept there.
func init() { runtime.LockOSThread() }

var menu struct {
	cfg config
	log *slog.Logger
}

func menubarCommand(ctx context.Context, cfg config, log *slog.Logger) error {
	menu.cfg, menu.log = cfg, log
	// The AppKit loop never returns, so a stop from launchd ends the process from here.
	go func() {
		<-ctx.Done()
		os.Exit(0)
	}()
	C.runMenuBar()
	return nil
}
