package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// holderOf is " (pid N)" from what the holder wrote, or "" when there is
// nothing readable there.
func holderOf(f *os.File) string {
	b := make([]byte, 32)
	n, _ := f.ReadAt(b, 0)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b[:n])))
	if err != nil || pid <= 0 {
		return ""
	}
	return fmt.Sprintf(" (pid %d)", pid)
}
