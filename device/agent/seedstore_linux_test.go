//go:build linux

package main

import (
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
)

// The whole Linux rule rests on this one distinction. A Secret Service that is not there
// is a seed file, because it is never coming. A collection that is locked is a wait,
// because generating a key would replace the machine's identity.
func TestALockedKeystoreIsNotAMissingOne(t *testing.T) {
	for name, err := range map[string]error{
		"org.freedesktop.DBus.Error.ServiceUnknown": dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"},
		"org.freedesktop.DBus.Error.NameHasNoOwner": dbus.Error{Name: "org.freedesktop.DBus.Error.NameHasNoOwner"},
	} {
		if !secretServiceMissing(err) {
			t.Errorf("%s: treated as a keystore that exists, want a seed file", name)
		}
	}
	for name, err := range map[string]error{
		"a locked collection": dbus.Error{Name: "org.freedesktop.Secret.Error.IsLocked"},
		"a prompt dismissed":  dbus.Error{Name: "org.freedesktop.DBus.Error.NoReply"},
		"anything else":       errors.New("connection reset"),
	} {
		if secretServiceMissing(err) {
			t.Errorf("%s: treated as no keystore at all, want a wait", name)
		}
	}
}
