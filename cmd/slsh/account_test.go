package main

// The account command, the one place an avatar's email address is asked
// for or printed.  Every address here is on example.invalid.
// Why: doc/account.md

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

const accountEmail = "somebody@example.invalid"

// TestAccountPrintsTheDetailsFromTheCapability.
func TestAccountPrintsTheDetailsFromTheCapability(t *testing.T) {
	x := newTestShell(t)
	x.grid.ServeCap(t, "UserInfo", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<llsd><map><key>success</key><boolean>true</boolean>`+
			`<key>email</key><string>%s</string>`+
			`<key>directory_visibility</key><string>default</string></map></llsd>`, accountEmail)
	})

	got := x.do(t, "account")
	if !strings.Contains(got, "email       "+accountEmail+"\n") ||
		!strings.Contains(got, "directory   default\n") {
		t.Errorf("account printed %q", got)
	}
	if strings.Contains(got, "IM") || strings.Contains(got, "im_via") {
		t.Errorf("account printed the retired IM-to-email flag: %q", got)
	}
}

// TestAccountAsksOverTheCircuitWhenThereIsNoCapability.
func TestAccountAsksOverTheCircuitWhenThereIsNoCapability(t *testing.T) {
	x := newTestShell(t)
	x.grid.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.UserInfoRequest); !ok {
			return
		}
		r := &msg.UserInfoReply{}
		r.AgentData.AgentID = x.s.Me()
		r.UserData.EMail = []byte(accountEmail + "\x00")
		r.UserData.DirectoryVisibility = []byte("hidden\x00")
		r.UserData.IMViaEMail = true
		x.grid.Relay(t, r)
	}

	got := x.do(t, "account")
	if !strings.Contains(got, accountEmail) || !strings.Contains(got, "directory   hidden") {
		t.Errorf("account printed %q", got)
	}
}

// TestAccountRefusedSaysWhyAndPrintsNoAddress.
func TestAccountRefusedSaysWhyAndPrintsNoAddress(t *testing.T) {
	x := newTestShell(t)
	x.grid.ServeCap(t, "UserInfo", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<llsd><map><key>success</key><boolean>false</boolean>`+
			`<key>message</key><string>not today</string>`+
			`<key>email</key><string>%s</string></map></llsd>`, accountEmail)
	})
	got := x.do(t, "account")
	if !strings.Contains(got, "not today") || strings.Contains(got, "example.invalid") {
		t.Errorf("account printed %q", got)
	}
}

// TestNothingButAccountAsksForTheDetails: the other commands that
// describe the avatar neither read the capability nor send the request.
func TestNothingButAccountAsksForTheDetails(t *testing.T) {
	x := newTestShell(t)
	var asked atomic.Int32
	x.grid.ServeCap(t, "UserInfo", func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		fmt.Fprintf(w, `<llsd><map><key>success</key><boolean>true</boolean>`+
			`<key>email</key><string>%s</string></map></llsd>`, accountEmail)
	})
	x.grid.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.UserInfoRequest); ok {
			asked.Add(1)
		}
	}
	for _, line := range []string{"where", "status", "look", "maturity", "who", "profile", "agents"} {
		if got := x.do(t, line); strings.Contains(got, "example.invalid") {
			t.Errorf("%s printed the address", line)
		}
	}
	if asked.Load() != 0 {
		t.Error("a command other than account asked for the details")
	}
}
