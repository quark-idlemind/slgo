# The ground

A region sends its heightmap once, as `LayerData` messages in the first
seconds after the avatar arrives, and never again for the asking.
`agent.Terrain` keeps each land body as it came, for a viewer that
attaches later, and decodes it as it arrives into a height at every
whole metre. `Terrain.HeightAt` reads the ground at a point and
`Terrain.Highest` the highest it comes in a rectangle. `sl.Session`
asks for them with `Ground` and `HighestGround`, which a hosted session
sends to the daemon's `Ground` rpc and a direct one reads off its own
agent.

## Decoding the land

The decoder follows Firestorm, and a detail here that is not the
viewer's says so below.

- A body starts with a group header: a 16-bit stride, an 8-bit patch
  size and an 8-bit layer type (`decode_patch_group_header`,
  `llmessage/patch_code.cpp`). The viewer replaces the stride with its
  own (`newview/llsurface.cpp:841`) and goes by the message's layer
  type rather than the header's (`newview/llvlmanager.cpp:117-126`), so
  neither is used. Patches of 16 and 32 are decoded; nothing else is.
- Each patch has a header: `quant_wbits` (8 bits), the DC offset (a
  32-bit float), the range (16 bits) and the patch id
  (`decode_patch_header`). A `quant_wbits` of 97 ends the body. The id
  is 10 bits on the `L` layer, x in the top five and y in the bottom
  five; on the extended `M` layer it is 32 bits, x in the high half and
  y in the low (`llsurface.cpp:858-869`).
- The coefficients follow in zigzag order: a 0 bit is a zero, `10`
  ends the patch, `11` is a value, then its sign, then `wbits` bits of
  it, where `wbits` is the low nibble of `quant_wbits` plus 2
  (`decode_patch`). Bits are read most significant first, and a value
  wider than a byte a byte at a time from its low byte
  (`LLBitPack::bitUnpack`, `llcommon/llbitpack.h:141`).
- Each is multiplied by `1 + 2(i + j)`, put back in its place, and
  transformed by the inverse DCT, columns and then rows. The height is
  that times `range / 2^prequant`, plus `range / 2`, plus the DC
  offset, where `prequant` is the high nibble of `quant_wbits` plus 2
  (`decompress_patch`, `llmessage/patch_idct.cpp:590`).
- A patch's id places its south west corner 16 metres a step from the
  region's, whatever size the patch is (`llsurface.cpp:1189`).
- Between the grid points each square is split into two triangles from
  its south west corner to its north east one, and the ground is flat
  on each: `LLSurface::resolveHeightRegion` (`llsurface.cpp:927-985`).

Where slgo does not do what the viewer does:

- A region is taken to be 256 metres across, as everywhere else in
  slgo. A patch placed further out ends the body, as the viewer ends
  one placed outside its own region (`llsurface.cpp:870-880`); on a
  larger `M` region the viewer would have taken it.
- A body that ends inside a patch ends there. The viewer reads on past
  the end of its buffer. In both, the patches before it stand.
- From 255 to 256 metres, east or north, the viewer reads the edge of
  the region beyond. slgo holds none, and holds the ground level there.
- A height is known only where the patches under its square have
  arrived. The viewer starts its ground at zero and draws that.

`Highest` is exact for that ground. The ground is flat on each
triangle, so its highest point in a rectangle is at a corner of the
rectangle, at a grid point inside it, or where an edge of the
rectangle crosses a grid line or a triangle's diagonal, and those are
the points it reads. A rectangle that reaches outside the region is not
known.

The heights take 256 KiB a session, beside the 40 KiB or so the bodies
take, and are decoded as each body arrives rather than when asked for:
a body dropped for `TerrainLimit` would otherwise take its land with
it.

## Checked against the grid

Measured on Agni on 2026-09-26, with a port of the viewer's decoder
written for the purpose: the land a session was sent on arrival came as
22 land messages, and they decoded into all 256 patches of the region.
Twelve 0.5 m boxes had been rezzed below the ground and set down on it
by the simulator (see [rez.md](rez.md#where-a-new-prim-lands)); the
decoded height under each matched its bottom to within 6 cm, most of
them within 3 cm.

The decoder in `agent` was not itself run against the grid. It was
checked offline against that port: on 256 patches of random
coefficients, encoded as the viewer's encoder lays them out, the two
gave the same height, to the bit, at 20,000 random points.
