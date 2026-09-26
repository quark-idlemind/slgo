package msg

// Where a region stands on the grid, and the number the protocol names
// it by.
//
// The grid is squares of 256 metres.  A region's grid coordinates --
// (995, 997), the pair the map speaks in -- are the square it occupies;
// its handle is the south-west corner of that square in metres, the x in
// the top half of a U64 and the y in the bottom (llregionhandle.h:34 and
// :136, to_region_handle over grid_to_region_handle).  The two are one
// fact written twice, and both spellings are needed in the same breath:
// MapBlockReply carries the coordinates and TeleportLocationRequest
// takes the handle.
//
// This is here rather than in sl because it is arithmetic over what is
// on the wire, and because agent needs it as much as sl does: the handle
// is how a region is named in a message, not something a client made up.
//
// Confirmed against Agni: the map places Sandbox Goguen at (995, 997),
// and the teleports there carried its handle as 1094014069892352, which
// is what RegionHandle gives for that pair.  See doc/history/teleport.md.

// regionWidth is how many metres a region is across, which is what
// separates the two spellings (indra_constants.h:38,
// REGION_WIDTH_UNITS).
const regionWidth = 256

// RegionHandle is the handle of the region whose grid coordinates these
// are.
func RegionHandle(x, y uint32) uint64 {
	return (uint64(x) * regionWidth << 32) | (uint64(y) * regionWidth)
}

// GridCoords is the other direction: the grid square a handle names.
//
// It exists to check the first -- a handle is opaque enough that the
// only way to read one is to take it apart -- and because arriving
// somewhere gives a handle where saying where that is wants the
// coordinates.
func GridCoords(handle uint64) (x, y uint32) {
	return uint32(handle>>32) / regionWidth, uint32(handle) / regionWidth
}
