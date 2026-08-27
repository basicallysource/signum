package engrave

import (
	"bytes"
	"fmt"
	"math"
	"testing"
)

func TestBinaryRoundTrip(t *testing.T) {
	m := plate()
	var buf bytes.Buffer
	if err := m.WriteBinary(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := Load(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Triangles) != len(m.Triangles) {
		t.Fatalf("got %d triangles, want %d", len(got.Triangles), len(m.Triangles))
	}
	// The fixture's coordinates are exact in float32, so the trip is exact.
	for i := range m.Triangles {
		if got.Triangles[i] != m.Triangles[i] {
			t.Fatalf("triangle %d changed: %v -> %v", i, m.Triangles[i], got.Triangles[i])
		}
	}
}

func TestWriteBinaryDeterministic(t *testing.T) {
	m := plate()
	var a, b bytes.Buffer
	if err := m.WriteBinary(&a); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteBinary(&b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("two writes of the same mesh differ")
	}
}

func TestASCIIRoundTrip(t *testing.T) {
	m := box(0, 0, 0, 10, 5, 2)
	var src bytes.Buffer
	fmt.Fprintln(&src, "solid fixture")
	for _, tr := range m.Triangles {
		n := tr.Normal()
		fmt.Fprintf(&src, "  facet normal %g %g %g\n    outer loop\n", n.X, n.Y, n.Z)
		for _, v := range tr {
			fmt.Fprintf(&src, "      vertex %g %g %g\n", v.X, v.Y, v.Z)
		}
		fmt.Fprintln(&src, "    endloop\n  endfacet")
	}
	fmt.Fprintln(&src, "endsolid fixture")

	got, err := Load(&src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Triangles) != len(m.Triangles) {
		t.Fatalf("got %d triangles, want %d", len(got.Triangles), len(m.Triangles))
	}
	for i := range m.Triangles {
		if got.Triangles[i] != m.Triangles[i] {
			t.Fatalf("triangle %d changed: %v -> %v", i, m.Triangles[i], got.Triangles[i])
		}
	}
}

func TestLoadRejectsJunk(t *testing.T) {
	if _, err := Load(bytes.NewReader([]byte("not a mesh at all"))); err == nil {
		t.Fatal("junk loaded without error")
	}
}

func TestVolumeAndBounds(t *testing.T) {
	m := plate()
	if v := m.Volume(); math.Abs(v-60*40*8) > 1e-9 {
		t.Fatalf("volume %v, want %v", v, 60*40*8)
	}
	min, max := m.Bounds()
	if min != (Vec3{0, 0, 0}) || max != (Vec3{60, 40, 8}) {
		t.Fatalf("bounds %v %v", min, max)
	}
	checkManifold(t, m)
}
