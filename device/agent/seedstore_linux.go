//go:build linux

package main

import (
	"errors"

	"github.com/godbus/dbus/v5"
)

// secretServiceMissing reports whether err is the Secret Service not being on the bus at
// all, which is a minimal desktop, a server or a container. That is permanent, so the
// agent uses a seed file rather than waiting for a keystore that will never appear. A
// collection that is merely locked reports something else and is worth waiting for.
func secretServiceMissing(err error) bool {
	var bus dbus.Error
	if !errors.As(err, &bus) {
		return false
	}
	switch bus.Name {
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner":
		return true
	}
	return false
}
