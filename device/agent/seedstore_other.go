//go:build !linux

package main

// Only Linux has the case this answers: see the file beside this one.
func secretServiceMissing(error) bool { return false }
