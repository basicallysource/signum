// Package engrave cuts a short line of text into a face of an STL as a
// shallow recessed pocket, producing a watertight STL.
//
// The point of the mark is that the physical object in your hand names
// itself: a part found in a drawer a year from now still says which design
// revision it is. The mark is recessed rather than raised because a pocket in
// the first layer prints cleanly on any printer and survives handling; the
// constants below encode what a person can actually read on FDM plastic, not
// what looks good on a screen.
//
// The flow is Load -> Placements -> Cut -> WriteBinary. Placements ranks
// every planar face the text fits on: the bed face first (a first-layer
// pocket prints cleanest and hides once assembled), then upward and vertical
// faces, downward overhangs last, and within a rank large faces before small
// ones. Each candidate carries a full 3D frame, so a viewer can fly a camera
// to the spot or paint the pocket without redoing any geometry. Cut then
// re-triangulates the chosen face locally around the text -- there is no
// general-purpose 3D boolean in pure Go, and none is needed, because the
// pocket only ever intrudes into one planar face.
//
// This version places on flat (planar) faces only. The natural next step is
// cylindrical and conical walls: both are developable, so a wall unrolls
// onto a plane without stretching, text keeps its true size and margins on
// it, and the pocket prism bends back onto the wall exactly. The Placement
// frame is already shaped so a curved face could carry one.
//
// Reproducibility is a hard constraint: the same mesh and text must produce
// the same bytes on every run, or a store of generated files fills with
// near-duplicates. The font is one pinned file fetched by hash, every
// iteration in the package walks slices in a fixed order (never a Go map),
// and placement search scans candidates on a fixed raster.
package engrave

// The engraving constants. These were settled by measurement on printed
// parts, not by taste, and they are exported so a caller can reason about
// them (a UI showing "3.5 mm text, 0.6 mm deep" should not hard-code its own
// copy).
const (
	// CapHeight is the height of a capital letter in mm: the smallest a
	// person reliably reads on plastic held in hand.
	CapHeight = 3.5
	// CapHeightSmall is the fallback when nothing on a part takes the full
	// size.
	CapHeightSmall = 2.5

	// PocketDepth is three 0.2 mm layers. A pocket two layers deep reads as
	// a smudge; three is the floor for legibility.
	PocketDepth = 0.6
	// PocketDepthShallow is two layers: the fallback for a sheet too thin to
	// give up three.
	PocketDepthShallow = 0.4

	// MinWall is the material required behind a full-depth pocket: 0.6 mm
	// removed, five printed layers left standing.
	MinWall = 1.6
	// MinWallShallow is the requirement under a shallow pocket: three layers
	// left.
	MinWallShallow = 1.0

	// PlaneTol is how far a vertex may sit from a facet's fitted plane and
	// still belong to it, mm. STL vertices are float32, so a nominally flat
	// face wobbles by rounding; this absorbs that without absorbing a bevel.
	PlaneTol = 0.02
	// NormalTol is the cosine two triangle normals must reach to count as
	// the same facet: 0.9995 is about 1.8 degrees.
	NormalTol = 0.9995

	// LargeFace, mm2: a flat face at least this big outranks small faces
	// regardless of area ordering within its rank.
	LargeFace = 600.0
)

// maxFacets bounds how many faces Placements considers, largest first. A
// part with more than two dozen distinct planar faces is not going to hide a
// better spot in the twenty-fifth.
const maxFacets = 24

// Options adjusts Placements. The zero value is the intended configuration;
// the engraving dimensions themselves are package constants on purpose, so
// that every caller's output is interchangeable.
type Options struct {
	// MaxPlacements caps how many ranked candidates are returned. Zero means
	// all of them.
	MaxPlacements int
}

// margin is the clear space demanded around the text on a face, scaled with
// the text so small text can use small faces.
func margin(cap float64) float64 { return 0.5 + cap*0.25 }
