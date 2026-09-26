package sl

import "context"

// Ground is the height of the land at a point in the region the avatar
// is in, in metres from its south west corner, and whether the land
// there has arrived.
//
// It is read from the heightmap the region sent when the avatar
// arrived, decoded as a viewer decodes it, and between the grid points
// it is the surface a viewer draws.  See agent.Terrain.HeightAt.
func (w *Session) Ground(ctx context.Context, x, y float32) (float32, bool, error) {
	return w.b.Ground(ctx, x, y, x, y)
}

// HighestGround is the highest the land comes anywhere in a rectangle
// of the region, and whether all of the land under it has arrived.  A
// rectangle that reaches outside the region is not known: the ground
// there is another region's.
func (w *Session) HighestGround(ctx context.Context, west, south, east, north float32) (float32, bool, error) {
	return w.b.Ground(ctx, west, south, east, north)
}
