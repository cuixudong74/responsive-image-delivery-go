package delivery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/jpeg"
	"image/png"
	"sync"
)

// Source is an immutable snapshot of a source image.
type Source struct {
	ID      string
	Data    []byte // encoded source bytes
	Hash    string // content hash, hex sha256 (truncated)
	Version uint64
}

// hashBytes returns the content hash used in cache keys.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// SourceStore holds source images. Updates are atomic: a reader that
// already resolved a Source keeps a coherent snapshot (same bytes,
// same hash, same version); it never observes a mix of versions.
type SourceStore struct {
	mu       sync.RWMutex
	sources  map[string]*Source
	versions map[string]uint64
}

// NewSourceStore creates an empty store.
func NewSourceStore() *SourceStore {
	return &SourceStore{sources: map[string]*Source{}, versions: map[string]uint64{}}
}

// Put stores data under id, replacing any previous version. The data
// is copied so callers cannot mutate a stored snapshot.
func (s *SourceStore) Put(id string, data []byte) *Source {
	cp := append([]byte(nil), data...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versions[id]++
	src := &Source{ID: id, Data: cp, Hash: hashBytes(cp), Version: s.versions[id]}
	s.sources[id] = src
	return src
}

// Get returns the current snapshot for id, or nil.
func (s *SourceStore) Get(id string) *Source {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sources[id]
}

// decodeSource decodes src.Data into an image. Supported source
// encodings are the registered wire formats (jpeg/png/rimg).
func decodeSource(src *Source) (image.Image, Format, error) {
	for _, f := range []Format{FormatRIMG, FormatJPEG, FormatPNG} {
		img, err := codecFor(f).Decode(src.Data)
		if err == nil {
			return img, f, nil
		}
	}
	// Last resort: stdlib sniffing for formats registered elsewhere.
	img, _, err := image.Decode(bytes.NewReader(src.Data))
	if err != nil {
		return nil, "", newError(ReasonGenerationFailed,
			"source %q (v%d) is not a decodable image", src.ID, src.Version)
	}
	return img, "", nil
}

// ensure image/jpeg and image/png decoders are linked for image.Decode.
var (
	_ = jpeg.Decode
	_ = png.Decode
)
