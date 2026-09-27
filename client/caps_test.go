package client

import (
	"context"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// reestablished is the agent as the daemon describes it once the session
// under the stream has been logged in again: a new session id, and a
// capability list that is not the one given at attach.
func reestablished() *pb.AgentInfo {
	return &pb.AgentInfo{
		Name: "quark", AgentId: testAgentID.String(),
		SessionId:  "59677e57-7e57-c0de-787f-aa3e9d2610f3",
		AvatarName: "Quark Idlemind", Region: "Test Region",
		Caps: []string{"SimulatorFeatures", "InventoryAPIv3"},
	}
}

// regionChanged relays a region change and waits for it to come out, so
// that the connection has heard it before the test goes on.
func regionChanged(t *testing.T, d *fakeDaemon, conn *Conn, detail string) {
	t.Helper()
	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Notice{Notice: &pb.AgentEvent{
		Kind: pb.AgentEvent_REGION_CHANGED, Detail: detail, Region: "Test Region",
	}}}
	select {
	case <-conn.RegionChanges():
	case <-time.After(5 * time.Second):
		t.Fatal("the region change never came out")
	}
}

// TestARegionChangeRefreshesTheCapabilities: a session re-established
// under the stream arrives as a region change, and the capabilities held
// from the attach are then another session's.  The next HasCap asks the
// daemon again.
func TestARegionChangeRefreshesTheCapabilities(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)
	if !conn.HasCap("ViewerAsset") || conn.HasCap("InventoryAPIv3") {
		t.Fatalf("attached with %v", conn.Caps())
	}

	d.status = &pb.StatusResponse{Agent: reestablished()}
	regionChanged(t, d, conn, "session re-established")

	if !conn.HasCap("InventoryAPIv3") || conn.HasCap("ViewerAsset") {
		t.Errorf("after the change HasCap still answers from %v", conn.Caps())
	}
	if got := conn.Info().GetSessionId(); got != reestablished().SessionId {
		t.Errorf("Info still has session %s", got)
	}

	// Once asked, not again until the next change.
	_, before := d.asked()
	conn.HasCap("InventoryAPIv3")
	conn.Caps()
	if _, after := d.asked(); after != before {
		t.Errorf("asked the daemon %d more times with nothing changed", after-before)
	}
}

// TestAFailedRefreshIsTriedAgain: a daemon that cannot answer leaves the
// old list in place, and still marked, so the next use asks again.
func TestAFailedRefreshIsTriedAgain(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)
	regionChanged(t, d, conn, "the avatar is now in Test Region")

	// No status set: the fake answers with nothing, which is no answer.
	if !conn.HasCap("ViewerAsset") {
		t.Error("the old list was thrown away for want of a new one")
	}
	d.status = &pb.StatusResponse{Agent: reestablished()}
	if !conn.HasCap("InventoryAPIv3") {
		t.Error("the next use did not ask again")
	}
}

// TestACapabilityTheGridNoLongerKnowsIsAskedForAgain: a named capability
// answered 404 "cap not found" is made again once, after asking the
// daemon who the agent is now.
func TestACapabilityTheGridNoLongerKnowsIsAskedForAgain(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)
	d.status = &pb.StatusResponse{Agent: reestablished()}
	d.capAnswers = []*pb.CapResponse{{Status: 404, Body: []byte("cap not found\n")}}
	d.capResp = &pb.CapResponse{Status: 200, Body: []byte("<llsd><map/></llsd>")}

	resp, err := conn.DoCap(context.Background(), agent.CapRequest{Cap: "InventoryAPIv3", Path: "/category/x"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 {
		t.Errorf("status %d, want the second answer's", resp.Status)
	}
	if caps, status := d.asked(); caps != 2 || status != 1 {
		t.Errorf("%d capability requests and %d status requests, want 2 and 1", caps, status)
	}
	if !conn.HasCap("InventoryAPIv3") {
		t.Error("the list was not refreshed along the way")
	}
}

// TestOnlyCapNotFoundIsAskedForAgain: any other 404 is an answer about
// the thing asked for, and a URL would be asked for unchanged; both are
// handed back as they came, and asked once.
func TestOnlyCapNotFoundIsAskedForAgain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		what string
		req  agent.CapRequest
		body string
	}{
		{"a 404 about the thing asked for", agent.CapRequest{Cap: "InventoryAPIv3"}, "no such category"},
		{"a URL", agent.CapRequest{URL: "https://example.invalid/upload"}, "cap not found"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			d, conn := attachFake(t)
			d.status = &pb.StatusResponse{Agent: reestablished()}
			d.capResp = &pb.CapResponse{Status: 404, Body: []byte(tc.body)}
			resp, err := conn.DoCap(context.Background(), tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != 404 {
				t.Errorf("status %d", resp.Status)
			}
			if caps, status := d.asked(); caps != 1 || status != 0 {
				t.Errorf("%d capability requests and %d status requests, want 1 and 0", caps, status)
			}
		})
	}
}
