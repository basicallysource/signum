package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

func TestPutOnceReadBack(t *testing.T) {
	store := Dir{Root: t.TempDir()}

	content := "solid nothing\nendsolid nothing\n"
	sum, size, err := store.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(content))
	if sum != hex.EncodeToString(want[:]) {
		t.Fatalf("sum %s does not match the content", sum)
	}
	if size != int64(len(content)) {
		t.Fatalf("size %d, want %d", size, len(content))
	}

	// The same bytes again land in the same place without complaint.
	again, _, err := store.Put(strings.NewReader(content))
	if err != nil || again != sum {
		t.Fatalf("second put: %s %v", again, err)
	}

	r, err := store.Open(sum)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	back, _ := io.ReadAll(r)
	if string(back) != content {
		t.Fatalf("read back %q", back)
	}
}

func TestOpenRejectsNonHashes(t *testing.T) {
	store := Dir{Root: t.TempDir()}
	for _, bad := range []string{"", "..", "../../etc/passwd", "ZZ", strings.Repeat("g", 64)} {
		if _, err := store.Open(bad); err == nil {
			t.Errorf("Open(%q) succeeded", bad)
		}
	}
}
