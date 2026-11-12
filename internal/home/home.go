// Package home finds the user's home directory, the one ~/.config and
// the rest are under.
package home

import (
	"errors"
	"os"
)

// Dir is $HOME when it is set, on every platform, and otherwise what
// os.UserHomeDir says -- which on Unix is $HOME again and on Windows is
// %USERPROFILE%.  A $HOME that is set and empty means there is none.
//
// Honoring $HOME on Windows keeps one rule everywhere: a shell from Git
// for Windows sets it to the profile directory anyway, and a test that
// points it at a temporary directory moves every path under it.
func Dir() (string, error) {
	if h, ok := os.LookupEnv("HOME"); ok {
		if h == "" {
			return "", errors.New("$HOME is empty")
		}
		return h, nil
	}
	return os.UserHomeDir()
}
