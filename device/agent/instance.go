package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// lockInstance keeps a second copy of a command from running on this PC. Two agents on
// one directory would hold one device key and dial the server twice, and two menu bar
// items would show two icons. The OS releases the lock when the process ends, a crash
// included, so nothing is ever left to clean up. Keep the file open for as long as the
// process runs.
func lockInstance(dir, name string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("agent %s is already running on this PC (%w)", name, err)
	}
	return f, nil
}
