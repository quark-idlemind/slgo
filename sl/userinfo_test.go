package sl

// An avatar's own account details.  The address in every fixture is
// on example.invalid, and a failure says that a field did not match,
// never what it was.
// Why: doc/account.md

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

const testEmail = "somebody@example.invalid"

// userInfoReplyFor builds a UserInfoReply for an agent.
func userInfoReplyFor(who msg.UUID, email, visibility string, im bool) *msg.UserInfoReply {
	r := &msg.UserInfoReply{}
	r.AgentData.AgentID = who
	r.UserData.EMail = []byte(email + "\x00")
	r.UserData.DirectoryVisibility = []byte(visibility + "\x00")
	r.UserData.IMViaEMail = im
	return r
}

// TestUserInfoByCapabilityReadsTheLLSDAndSendsNoUDP: the region offers
// UserInfo, so it is a GET and nothing goes on the circuit.
func TestUserInfoByCapabilityReadsTheLLSDAndSendsNoUDP(t *testing.T) {
	w, f := newFakeSession(t)
	var method atomic.Value
	f.ServeCap(t, UserInfoCap, func(rw http.ResponseWriter, r *http.Request) {
		method.Store(r.Method)
		fmt.Fprintf(rw, `<llsd><map><key>success</key><boolean>true</boolean>`+
			`<key>email</key><string>%s</string>`+
			`<key>directory_visibility</key><string>hidden</string></map></llsd>`, testEmail)
	})

	u, err := w.UserInfo(context.Background())
	if err != nil {
		t.Fatalf("UserInfo: %v", err)
	}
	if u.Email != testEmail {
		t.Error("the email did not match")
	}
	if u.DirectoryVisibility != "hidden" {
		t.Error("the directory visibility did not match")
	}
	if method.Load() != "GET" {
		t.Errorf("the capability was asked with %v", method.Load())
	}
	for _, s := range f.Sent() {
		if _, ok := s.Msg.(*msg.UserInfoRequest); ok {
			t.Error("a UserInfoRequest went out although the capability was offered")
		}
	}
}

// TestUserInfoByCapabilityRefusedIsAnErrorWithTheMessageOnly:
// success false is a failure, carrying message, and the error never
// holds the rest of the body.
func TestUserInfoByCapabilityRefusedIsAnErrorWithTheMessageOnly(t *testing.T) {
	w, f := newFakeSession(t)
	f.ServeCap(t, UserInfoCap, func(rw http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(rw, `<llsd><map><key>success</key><boolean>false</boolean>`+
			`<key>message</key><string>not today</string>`+
			`<key>email</key><string>%s</string></map></llsd>`, testEmail)
	})

	_, err := w.UserInfo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not today") {
		t.Fatalf("err = %v, want the grid's message", err)
	}
	if strings.Contains(err.Error(), "example.invalid") {
		t.Error("the error carried the response body")
	}
}

// TestUserInfoErrorsNeverCarryTheBody: a status, a body that is not
// LLSD, and one that is not a map each say what failed and nothing of
// what was received.
func TestUserInfoErrorsNeverCarryTheBody(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"status": func(rw http.ResponseWriter, r *http.Request) {
			http.Error(rw, testEmail, http.StatusInternalServerError)
		},
		"not llsd": func(rw http.ResponseWriter, r *http.Request) {
			fmt.Fprint(rw, testEmail)
		},
		"not a map": func(rw http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(rw, `<llsd><string>%s</string></llsd>`, testEmail)
		},
	} {
		t.Run(name, func(t *testing.T) {
			w, f := newFakeSession(t)
			f.ServeCap(t, UserInfoCap, h)
			_, err := w.UserInfo(context.Background())
			if err == nil {
				t.Fatal("UserInfo succeeded")
			}
			if strings.Contains(err.Error(), "example.invalid") {
				t.Error("the error carried the response body")
			}
		})
	}
}

// TestUserInfoWithoutTheCapabilityAsksOverTheCircuit: the request names
// this agent and session, and the reply's fields come back.  A reply for
// another agent is not the answer.
func TestUserInfoWithoutTheCapabilityAsksOverTheCircuit(t *testing.T) {
	w, f := newFakeSession(t)
	other := msg.MustParseUUID("e1a07e57-7e57-c0de-2bef-9e42ac343dd1")

	var asked *msg.UserInfoRequest
	f.onSend = func(m msg.Message) {
		req, ok := m.(*msg.UserInfoRequest)
		if !ok {
			return
		}
		asked = req
		f.Relay(t, userInfoReplyFor(other, "elsewhere@example.invalid", "other", false))
		f.Relay(t, userInfoReplyFor(w.me, testEmail, "default", true))
	}

	u, err := w.UserInfo(context.Background())
	if err != nil {
		t.Fatalf("UserInfo: %v", err)
	}
	if asked == nil || asked.AgentData.AgentID != w.me || asked.AgentData.SessionID != w.Session() {
		t.Error("the request did not carry this agent and session")
	}
	if u.Email != testEmail || u.DirectoryVisibility != "default" || !u.IMViaEmail {
		t.Error("the reply for this agent was not the answer")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	for _, fn := range w.userInfoFns {
		if fn != nil {
			t.Error("a waiter was left behind")
		}
	}
}

// TestUserInfoWithNoReplyTimesOutWrappingErrTimeout.
func TestUserInfoWithNoReplyTimesOutWrappingErrTimeout(t *testing.T) {
	w, _ := newFakeSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := w.UserInfo(ctx)
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
}

// TestUserInfoReplyIsBorrowedOnlyWhileAsking: it is not among the
// standing Subscriptions, so a session that did not ask is never sent
// the account's email; a call watches it for its own wait and gives it
// back.
// Why: doc/account.md#nothing-logs-it
func TestUserInfoReplyIsBorrowedOnlyWhileAsking(t *testing.T) {
	for _, s := range Subscriptions {
		if s == "UserInfoReply" {
			t.Error("UserInfoReply is a standing subscription")
		}
	}

	w, f := newFakeSession(t)
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.UserInfoRequest); ok {
			if !f.Watching("UserInfoReply") {
				t.Error("the request went out before the reply was watched")
			}
			f.Relay(t, userInfoReplyFor(w.me, testEmail, "default", false))
		}
	}
	if _, err := w.UserInfo(context.Background()); err != nil {
		t.Fatalf("UserInfo: %v", err)
	}
	if f.Watching("UserInfoReply") {
		t.Error("the reply is still watched after the call")
	}
	got := f.Watched()
	if len(got) != 2 || got[0] != "+UserInfoReply" || got[1] != "-UserInfoReply" {
		t.Errorf("watched %v, want +UserInfoReply then -UserInfoReply", got)
	}
}
