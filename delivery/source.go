package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"sync"
)

// Source 是一份源图：内容字节、内容哈希（版本标识）与解码后的尺寸。
// 内容哈希即版本，源图更新会产生新的哈希，从而自动失效旧派生。
type Source struct {
	ID     string
	Data   []byte
	Hash   string // 内容哈希（sha256 hex）
	Width  int
	Height int
	Format Format // 源图原始格式
}

// SourceStore 以 ID 存放源图，Put 相同 ID 不同内容即视为源图更新。
type SourceStore struct {
	mu      sync.RWMutex
	sources map[string]Source
}

// NewSourceStore 构造空源图仓库。
func NewSourceStore() *SourceStore { return &SourceStore{sources: map[string]Source{}} }

// Put 存入或替换源图，返回解析后的 Source（含哈希与尺寸）。
// data 会被拷贝，调用方后续修改不影响仓库内容。
func (s *SourceStore) Put(id string, data []byte) (Source, error) {
	img, format, err := DecodeImage(data)
	if err != nil {
		return Source{}, err
	}
	sum := sha256.Sum256(data)
	cp := make([]byte, len(data))
	copy(cp, data)
	src := Source{
		ID:     id,
		Data:   cp,
		Hash:   hex.EncodeToString(sum[:]),
		Width:  img.Bounds().Dx(),
		Height: img.Bounds().Dy(),
		Format: format,
	}
	s.mu.Lock()
	s.sources[id] = src
	s.mu.Unlock()
	return src, nil
}

// Get 读取源图快照；读到的要么是完整的旧版本，要么是完整的新版本，
// 不会出现尺寸与内容错配的混合结果。
func (s *SourceStore) Get(id string) (Source, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src, ok := s.sources[id]
	return src, ok
}

// Decode 解码源图内容。
func (s Source) Decode() (image.Image, error) {
	img, _, err := DecodeImage(s.Data)
	if err != nil {
		return nil, fmt.Errorf("delivery: decode source %s: %w", s.ID, err)
	}
	return img, nil
}
