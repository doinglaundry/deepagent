package backends

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Store 持久化存储接口
// 提供键值对存储能力，支持跨会话持久化
type Store interface {
	// Get 获取指定键的值
	Get(ctx context.Context, key string) ([]byte, error)

	// Set 设置指定键的值
	Set(ctx context.Context, key string, value []byte) error

	// Delete 删除指定键
	Delete(ctx context.Context, key string) error

	// List 列出指定前缀的所有键
	List(ctx context.Context, prefix string) ([]string, error)

	// Exists 检查键是否存在
	Exists(ctx context.Context, key string) (bool, error)

	// Close 关闭存储
	Close() error
}

// StoreBackendConfig StoreBackend 配置
type StoreBackendConfig struct {
	// Store 底层存储实现
	Store Store

	// AssistantID 助手 ID，用于隔离不同助手的数据
	AssistantID string

	// Namespace 命名空间，用于进一步隔离数据
	Namespace string

	// TTL 数据过期时间，0 表示永不过期
	TTL time.Duration
}

// StoreBackend 基于 Store 的后端实现
// 提供跨会话的持久化存储能力
type StoreBackend struct {
	config      *StoreBackendConfig
	store       Store
	keyPrefix   string
	virtualRoot string
	mu          sync.RWMutex
}

// NewStoreBackend 创建 StoreBackend
func NewStoreBackend(config *StoreBackendConfig) *StoreBackend {
	if config == nil {
		config = &StoreBackendConfig{}
	}

	// 构建键前缀
	var prefixParts []string
	if config.AssistantID != "" {
		prefixParts = append(prefixParts, config.AssistantID)
	}
	if config.Namespace != "" {
		prefixParts = append(prefixParts, config.Namespace)
	}
	keyPrefix := strings.Join(prefixParts, "/")
	if keyPrefix != "" {
		keyPrefix += "/"
	}

	return &StoreBackend{
		config:      config,
		store:       config.Store,
		keyPrefix:   keyPrefix,
		virtualRoot: "/",
	}
}

// makeKey 构建存储键
func (b *StoreBackend) makeKey(path string) string {
	// 规范化路径
	path = filepath.Clean(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// 移除开头的 /
	path = strings.TrimPrefix(path, "/")
	return b.keyPrefix + "files/" + path
}

// makeMetaKey 构建元数据键
func (b *StoreBackend) makeMetaKey(path string) string {
	path = filepath.Clean(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.TrimPrefix(path, "/")
	return b.keyPrefix + "meta/" + path
}

// FileMetadata 文件元数据
type FileMetadata struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	IsDir     bool      `json:"is_dir"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Read 读取文件内容
func (b *StoreBackend) Read(ctx context.Context, path string, offset, limit *int) (string, error) {
	if b.store == nil {
		return "", fmt.Errorf("store not configured")
	}

	key := b.makeKey(path)
	data, err := b.store.Get(ctx, key)
	if err != nil {
		return "", err
	}

	content := string(data)

	// 处理分页（仅在指定 offset 或 limit 时）
	if offset != nil || limit != nil {
		actualOffset := 0
		if offset != nil {
			actualOffset = *offset
		}

		lines := strings.Split(content, "\n")
		if actualOffset >= len(lines) {
			return "", nil
		}
		end := len(lines)
		if limit != nil && *limit > 0 && actualOffset+*limit < end {
			end = actualOffset + *limit
		}
		lines = lines[actualOffset:end]
		content = strings.Join(lines, "\n")
	}

	return content, nil
}

// Write 写入文件
func (b *StoreBackend) Write(ctx context.Context, path, content string) (*WriteResult, error) {
	if b.store == nil {
		return &WriteResult{Path: path, Error: ErrInvalidPath}, nil
	}

	key := b.makeKey(path)
	err := b.store.Set(ctx, key, []byte(content))
	if err != nil {
		return &WriteResult{Path: path, Error: ErrPermissionDenied}, nil
	}

	// 更新元数据
	meta := &FileMetadata{
		Path:      path,
		Size:      int64(len(content)),
		IsDir:     false,
		UpdatedAt: time.Now(),
	}
	if existingMeta, _ := b.getMeta(ctx, path); existingMeta != nil {
		meta.CreatedAt = existingMeta.CreatedAt
	} else {
		meta.CreatedAt = time.Now()
	}
	_ = b.setMeta(ctx, path, meta)

	// 确保父目录存在
	b.ensureParentDirs(ctx, path)

	return &WriteResult{
		Path: path,
	}, nil
}

// Edit 编辑文件
func (b *StoreBackend) Edit(ctx context.Context, path, oldStr, newStr string, all bool) (*EditResult, error) {
	content, err := b.Read(ctx, path, nil, nil)
	if err != nil {
		return &EditResult{Error: ErrFileNotFound}, nil
	}

	var newContent string
	var count int
	if all {
		count = strings.Count(content, oldStr)
		newContent = strings.ReplaceAll(content, oldStr, newStr)
	} else {
		if strings.Contains(content, oldStr) {
			newContent = strings.Replace(content, oldStr, newStr, 1)
			count = 1
		} else {
			return &EditResult{
				Path:        path,
				Occurrences: 0,
			}, nil
		}
	}

	if count == 0 {
		return &EditResult{
			Path:        path,
			Occurrences: 0,
		}, nil
	}

	_, err = b.Write(ctx, path, newContent)
	if err != nil {
		return &EditResult{Error: ErrInvalidPath}, nil
	}

	return &EditResult{
		Path:        path,
		Occurrences: count,
	}, nil
}

// Ls 列出目录内容
func (b *StoreBackend) Ls(ctx context.Context, path string) ([]string, error) {
	if b.store == nil {
		return nil, fmt.Errorf("store not configured")
	}

	// 规范化路径
	path = filepath.Clean(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}

	prefix := b.keyPrefix + "files" + path
	keys, err := b.store.List(ctx, prefix)
	if err != nil {
		return nil, err
	}

	// 提取直接子目录和文件
	seen := make(map[string]bool)
	var result []string
	for _, key := range keys {
		// 移除前缀
		relPath := strings.TrimPrefix(key, prefix)
		if relPath == "" {
			continue
		}
		// 只取第一级
		parts := strings.SplitN(relPath, "/", 2)
		name := parts[0]
		if !seen[name] {
			seen[name] = true
			result = append(result, filepath.Join(path, name))
		}
	}

	return result, nil
}

// LsInfo 列出目录内容（带文件信息）
func (b *StoreBackend) LsInfo(ctx context.Context, path string) ([]*FileInfo, error) {
	paths, err := b.Ls(ctx, path)
	if err != nil {
		return nil, err
	}

	var infos []*FileInfo
	for _, p := range paths {
		meta, _ := b.getMeta(ctx, p)
		info := &FileInfo{
			Path: p,
		}
		if meta != nil {
			info.Size = meta.Size
			info.IsDir = meta.IsDir
		} else {
			// 检查是否是目录
			children, _ := b.Ls(ctx, p)
			info.IsDir = len(children) > 0
		}
		infos = append(infos, info)
	}

	return infos, nil
}

// Glob 匹配文件
func (b *StoreBackend) Glob(ctx context.Context, pattern, basePath string) ([]string, error) {
	if b.store == nil {
		return nil, fmt.Errorf("store not configured")
	}

	// 列出所有文件
	prefix := b.keyPrefix + "files/"
	keys, err := b.store.List(ctx, prefix)
	if err != nil {
		return nil, err
	}

	var result []string
	for _, key := range keys {
		path := "/" + strings.TrimPrefix(key, prefix)
		if !strings.HasPrefix(path, basePath) {
			continue
		}
		relPath := strings.TrimPrefix(path, basePath)
		if strings.HasPrefix(relPath, "/") {
			relPath = relPath[1:]
		}
		matched, _ := filepath.Match(pattern, filepath.Base(path))
		if matched {
			result = append(result, path)
		}
	}

	return result, nil
}

// GlobInfo 匹配文件（带文件信息）
func (b *StoreBackend) GlobInfo(ctx context.Context, pattern, basePath string) ([]*FileInfo, error) {
	paths, err := b.Glob(ctx, pattern, basePath)
	if err != nil {
		return nil, err
	}

	var infos []*FileInfo
	for _, p := range paths {
		meta, _ := b.getMeta(ctx, p)
		info := &FileInfo{
			Path: p,
		}
		if meta != nil {
			info.Size = meta.Size
			info.IsDir = meta.IsDir
		}
		infos = append(infos, info)
	}

	return infos, nil
}

// GrepRaw 搜索文件内容
func (b *StoreBackend) GrepRaw(ctx context.Context, pattern, path, glob string) ([]*GrepMatch, error) {
	if b.store == nil {
		return nil, fmt.Errorf("store not configured")
	}

	// 获取要搜索的文件列表
	var files []string
	if glob != "" {
		var err error
		files, err = b.Glob(ctx, glob, path)
		if err != nil {
			return nil, err
		}
	} else {
		prefix := b.keyPrefix + "files" + path
		keys, err := b.store.List(ctx, prefix)
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			files = append(files, "/"+strings.TrimPrefix(key, b.keyPrefix+"files/"))
		}
	}

	var matches []*GrepMatch
	for _, file := range files {
		content, err := b.Read(ctx, file, nil, nil)
		if err != nil {
			continue
		}

		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if strings.Contains(line, pattern) {
				matches = append(matches, &GrepMatch{
					Path: file,
					Line: i + 1,
					Text: line,
				})
				if len(matches) >= 100 {
					return matches, nil
				}
			}
		}
	}

	return matches, nil
}

// 元数据操作

func (b *StoreBackend) getMeta(ctx context.Context, path string) (*FileMetadata, error) {
	if b.store == nil {
		return nil, fmt.Errorf("store not configured")
	}

	key := b.makeMetaKey(path)
	data, err := b.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}

	var meta FileMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}

	return &meta, nil
}

func (b *StoreBackend) setMeta(ctx context.Context, path string, meta *FileMetadata) error {
	if b.store == nil {
		return fmt.Errorf("store not configured")
	}

	key := b.makeMetaKey(path)
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}

	return b.store.Set(ctx, key, data)
}

func (b *StoreBackend) ensureParentDirs(ctx context.Context, path string) {
	dir := filepath.Dir(path)
	for dir != "/" && dir != "." {
		meta := &FileMetadata{
			Path:      dir,
			IsDir:     true,
			UpdatedAt: time.Now(),
		}
		if existing, _ := b.getMeta(ctx, dir); existing == nil {
			meta.CreatedAt = time.Now()
			_ = b.setMeta(ctx, dir, meta)
		}
		dir = filepath.Dir(dir)
	}
}

// Store 获取底层 Store
func (b *StoreBackend) Store() Store {
	return b.store
}

// KeyPrefix 获取键前缀
func (b *StoreBackend) KeyPrefix() string {
	return b.keyPrefix
}

// SetStore 设置底层 Store
func (b *StoreBackend) SetStore(store Store) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.store = store
}

// Close 关闭后端
func (b *StoreBackend) Close() error {
	if b.store != nil {
		return b.store.Close()
	}
	return nil
}
