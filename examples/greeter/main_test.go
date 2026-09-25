package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

func TestAsksForInfo(t *testing.T) {
	for text, want := range map[string]bool{
		"info":             true,
		"Info":             true,
		"  INFO  ":         true,
		"info?":            true,
		"info!":            true,
		"information":      false,
		"more info please": false,
		"":                 false,
	} {
		if got := asksForInfo(text); got != want {
			t.Errorf("asksForInfo(%q) = %v, want %v", text, got, want)
		}
	}
}

// TestMemoryKeepsVisitors: what is saved is what the next run loads,
// and a missing file is an empty memory rather than an error.
func TestMemoryKeepsVisitors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "greeted.json")

	m, err := loadMemory(path)
	if err != nil {
		t.Fatalf("no file yet: %v", err)
	}
	if len(m.Visitors) != 0 {
		t.Fatalf("no file yet, but %d visitors", len(m.Visitors))
	}

	first := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC)
	m.Visitors["c75d7e57-7e57-c0de-b372-000000000001"] = &visitor{Name: "Example Resident", First: first, Last: first}
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("saved as %v, %v; want mode 600", fi.Mode().Perm(), err)
	}
	if _, err := os.Stat(path + ".new"); !os.IsNotExist(err) {
		t.Errorf("the temporary file was left behind: %v", err)
	}

	again, err := loadMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	v := again.Visitors["c75d7e57-7e57-c0de-b372-000000000001"]
	if v == nil || v.Name != "Example Resident" || !v.First.Equal(first) {
		t.Errorf("loaded %+v", v)
	}
}

// TestTooSoon: the information is given to one person once in half a
// minute, and to somebody else regardless.
func TestTooSoon(t *testing.T) {
	last := map[msg.UUID]time.Time{}
	a := msg.UUID{0xbb, 1}
	b := msg.UUID{0xbb, 2}
	if tooSoon(last, a) {
		t.Fatal("the first ask was too soon")
	}
	if !tooSoon(last, a) {
		t.Error("a second ask straight away was answered")
	}
	if tooSoon(last, b) {
		t.Error("somebody else was refused")
	}
	last[a] = time.Now().Add(-31 * time.Second)
	if tooSoon(last, a) {
		t.Error("an ask after half a minute was refused")
	}
}
