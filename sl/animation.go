package sl

// Playing an animation, and stopping it.
//
// One message, AgentAnimation, carrying the animation's id and a flag
// that says start or stop, exactly as the viewer builds it
// (LLAgent::sendAnimationRequest, newview/llagent.cpp:3922).  The
// simulator answers nothing to it, so this returns when the message is
// sent and says nothing about whether the animation plays: a request
// for an animation the avatar cannot play, or has not the right to, is
// dropped without a word.  What the avatar is playing arrives in
// AvatarAnimation, which this package keeps only for the length of a
// sit or a stand (borrowAnimations), so it is not offered here.
//
// The id is an asset id.  For a built-in that is its constant; for one
// in inventory it is the item's asset, not the item's own id -- the
// inventory preview's play button sends item->getAssetUUID()
// (newview/llpreviewanim.cpp:85).
// Why: doc/animations.md#what-is-sent

import (
	"context"
	"fmt"
	"slices"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// StartAnimation asks for an animation, by asset id, to be played on
// this avatar.
func (w *Session) StartAnimation(ctx context.Context, id msg.UUID) error {
	return w.animate(ctx, id, true)
}

// StopAnimation asks for an animation, by asset id, to stop.
func (w *Session) StopAnimation(ctx context.Context, id msg.UUID) error {
	return w.animate(ctx, id, false)
}

func (w *Session) animate(ctx context.Context, id msg.UUID, start bool) error {
	// The viewer skips a null id (llagent.cpp:3924) and a null start
	// would ask the simulator for nothing.
	if id.IsZero() {
		return fmt.Errorf("sl: no animation to play; an animation is named by its asset id")
	}
	m := &msg.AgentAnimation{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AnimationList = []msg.AgentAnimation_AnimationList{{AnimID: id, StartAnim: start}}
	// The viewer sends one empty physical avatar event block too
	// (llagent.cpp:3939), so this does.
	m.PhysicalAvatarEventList = []msg.AgentAnimation_PhysicalAvatarEventList{{}}
	return w.Send(ctx, m)
}

// BuiltinAnimations is every named built-in, sorted by name.
func BuiltinAnimations() []agent.BuiltinAnimation { return slices.Clone(agent.BuiltinAnimations) }

// BuiltinAnimation is the built-in animation called name.  Names are
// the viewer's, in lower case, matched exactly like every inventory
// name; one that differs only in case is refused with the near miss
// named.  The refusal is a *NameError.
func BuiltinAnimation(name string) (agent.BuiltinAnimation, error) {
	a, err := PickNamedFunc(agent.BuiltinAnimations, name, "built-in animation", "",
		func(a agent.BuiltinAnimation) (string, msg.UUID) { return a.Name, a.ID })
	if err != nil {
		return agent.BuiltinAnimation{}, fmt.Errorf("sl: %w", err)
	}
	return a, nil
}

// AnimationAsset is the asset id to play for an inventory entry, which
// has to be an animation.  A link is refused: its asset is the item it
// points at, and following it is the caller's (the shell's linkTarget).
func AnimationAsset(e Entry) (msg.UUID, error) {
	switch {
	case e.Folder:
		return msg.UUID{}, fmt.Errorf("sl: %s is a folder, not an animation", e.Name)
	case e.IsLink:
		return msg.UUID{}, fmt.Errorf("sl: %s is a link; name the animation it points at", e.Name)
	case AssetType(e.Type) != AssetAnimation:
		return msg.UUID{}, fmt.Errorf("sl: %s is a %s, not an animation", e.Name, AssetType(e.Type))
	case e.Asset.IsZero():
		return msg.UUID{}, fmt.Errorf("sl: %s names no asset, so there is nothing to play", e.Name)
	}
	return e.Asset, nil
}

// InventoryAnimation is the animation at a path from the inventory
// root, or in the folder named by the path's leading names.  The last
// name picks out one item through PickNamed: exactly, in the case it
// has, and several of a name are refused with their ids.  Returns the
// entry; AnimationAsset says what to play for it.
func (w *Session) InventoryAnimation(ctx context.Context, path string) (Entry, error) {
	names := SplitPath(path)
	if len(names) == 0 {
		return Entry{}, fmt.Errorf("sl: no animation named")
	}
	es, err := w.ListInventory(ctx, JoinPath(names[:len(names)-1]...), 0)
	if err != nil {
		return Entry{}, err
	}
	var items []Entry
	for _, e := range es {
		if !e.Folder {
			items = append(items, e)
		}
	}
	e, err := PickNamed(items, names[len(names)-1], "animation", "in "+
		orRoot(JoinPath(names[:len(names)-1]...)))
	if err != nil {
		return Entry{}, fmt.Errorf("sl: %w", err)
	}
	if _, err := AnimationAsset(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

func orRoot(where string) string {
	if where == "" {
		return "the inventory root"
	}
	return where
}
