package client

import (
	"errors"

	"github.com/quark-idlemind/slgo/msg"
)

// ErrTransferDenied is reported when the simulator refuses an asset
// transfer.
var ErrTransferDenied = errors.New("client: transfer refused")

// ErrXferAborted is reported when the simulator gives up on a file
// transfer.
var ErrXferAborted = errors.New("client: transfer aborted")

// AssetRef says which asset to read and how to prove we may.
//
// Owner, Task and Item are what the simulator checks the permissions
// against.  Task is zero for something in agent inventory.
type AssetRef struct {
	Owner msg.UUID
	Task  msg.UUID
	Item  msg.UUID
	Asset msg.UUID
	Type  int32
}
