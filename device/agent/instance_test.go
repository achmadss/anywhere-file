package main

import (
	"strings"
	"testing"
)

func TestASecondInstanceIsRefusedUntilTheFirstEnds(t *testing.T) {
	dir := t.TempDir()
	first, err := lockInstance(dir, "run")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockInstance(dir, "run"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second run: %v, want already running", err)
	}
	other, err := lockInstance(dir, "menubar")
	if err != nil {
		t.Fatalf("the menu bar has its own lock: %v", err)
	}
	other.Close()
	first.Close()
	again, err := lockInstance(dir, "run")
	if err != nil {
		t.Fatalf("after the first ended: %v", err)
	}
	again.Close()
}
