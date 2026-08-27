// Package blob stores uploaded bytes exactly once, addressed by what they
// are. A file's name is the SHA-256 of its content, so the same STL uploaded
// twice costs nothing, references never dangle, and integrity is a re-hash.
//
// The interface is small on purpose: a hosted deployment can move to object
// storage behind the same three methods without touching a handler.
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Store is somewhere bytes can be kept and retrieved by hash.
type Store interface {
	// Put reads everything and returns the content's hex SHA-256 and size.
	Put(r io.Reader) (sum string, size int64, err error)
	// Open returns the content behind a hash.
	Open(sum string) (io.ReadCloser, error)
}

// Dir is a Store on the local filesystem: <root>/ab/<full hash>, the first
// byte split off so no single directory grows unbounded.
type Dir struct {
	Root string
}

// Put writes content through a temp file and renames it into place, so a
// crash can never leave a half-written blob under a valid name.
func (d Dir) Put(r io.Reader) (string, int64, error) {
	if err := os.MkdirAll(d.Root, 0o755); err != nil {
		return "", 0, fmt.Errorf("blob: make root: %w", err)
	}
	tmp, err := os.CreateTemp(d.Root, "incoming-*")
	if err != nil {
		return "", 0, fmt.Errorf("blob: temp file: %w", err)
	}
	defer os.Remove(tmp.Name())

	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hash), r)
	if err != nil {
		tmp.Close()
		return "", 0, fmt.Errorf("blob: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("blob: close: %w", err)
	}

	sum := hex.EncodeToString(hash.Sum(nil))
	dest := d.path(sum)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", 0, fmt.Errorf("blob: make shard: %w", err)
	}
	if _, err := os.Stat(dest); err == nil {
		// Already stored; the rename below would also be fine, but saying
		// so is clearer than relying on it.
		return sum, size, nil
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return "", 0, fmt.Errorf("blob: place %s: %w", sum, err)
	}
	return sum, size, nil
}

// Open returns the content behind a hash.
func (d Dir) Open(sum string) (io.ReadCloser, error) {
	if !validSum(sum) {
		return nil, fmt.Errorf("blob: %q is not a hash", sum)
	}
	f, err := os.Open(d.path(sum))
	if err != nil {
		return nil, fmt.Errorf("blob: open %s: %w", sum, err)
	}
	return f, nil
}

func (d Dir) path(sum string) string {
	return filepath.Join(d.Root, sum[:2], sum)
}

func validSum(sum string) bool {
	if len(sum) != 64 {
		return false
	}
	return strings.IndexFunc(sum, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
	}) == -1
}
