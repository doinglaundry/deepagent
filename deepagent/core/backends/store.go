package backends

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FileStore 基于文件系统的 Store 实现
type FileStore struct {
	rootDir string
	mu      sync.RWMutex
}

// FileStoreConfig FileStore 配置
type FileStoreConfig struct {
	// RootDir 存储根目录
	RootDir string
}

// NewFileStore 创建文件存储
func NewFileStore(config *FileStoreConfig) (*FileStore, error) {
	if config == nil {
		config = &FileStoreConfig{}
	}

	rootDir := config.RootDir
	if rootDir == "" {
		// 默认使用用户目录下的 .deepagents/store
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home dir: %w", err)
		}
		rootDir = filepath.Join(home, ".deepagents", "store")
	}

	// 确保目录存在
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create store dir: %w", err)
	}

	return &FileStore{
		rootDir: rootDir,
	}, nil
}

// keyToPath 将键转换为文件路径
func (s *FileStore) keyToPath(key string) string {
	// 替换不安全的字符
	safe := strings.ReplaceAll(key, ":", "_")
	return filepath.Join(s.rootDir, safe)
}

// Get 获取值
func (s *FileStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.keyToPath(key)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read key %s: %w", key, err)
	}

	return data, nil
}

// Set 设置值
func (s *FileStore) Set(ctx context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.keyToPath(key)

	// 确保目录存在
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create dir for key %s: %w", key, err)
	}

	if err := os.WriteFile(path, value, 0644); err != nil {
		return fmt.Errorf("failed to write key %s: %w", key, err)
	}

	return nil
}

// Delete 删除键
func (s *FileStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.keyToPath(key)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete key %s: %w", key, err)
	}

	return nil
}

// List 列出前缀匹配的键
func (s *FileStore) List(ctx context.Context, prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	safePrefix := strings.ReplaceAll(prefix, ":", "_")
	prefixPath := filepath.Join(s.rootDir, safePrefix)

	var keys []string
	err := filepath.Walk(s.rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 忽略错误，继续遍历
		}
		if info.IsDir() {
			return nil
		}

		// 检查是否匹配前缀
		relPath, err := filepath.Rel(s.rootDir, path)
		if err != nil {
			return nil
		}

		// 转换回键格式
		key := strings.ReplaceAll(relPath, "_", ":")

		if strings.HasPrefix(filepath.Join(s.rootDir, relPath), prefixPath) ||
			strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list keys: %w", err)
	}

	return keys, nil
}

// Exists 检查键是否存在
func (s *FileStore) Exists(ctx context.Context, key string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.keyToPath(key)
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check key %s: %w", key, err)
	}

	return true, nil
}

// Close 关闭存储
func (s *FileStore) Close() error {
	// 文件存储不需要关闭
	return nil
}

// RootDir 获取根目录
func (s *FileStore) RootDir() string {
	return s.rootDir
}

// Clear 清除所有数据
func (s *FileStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.rootDir)
	if err != nil {
		return fmt.Errorf("failed to read store dir: %w", err)
	}

	for _, entry := range entries {
		path := filepath.Join(s.rootDir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("failed to remove %s: %w", path, err)
		}
	}

	return nil
}

// MemoryStore 基于内存的 Store 实现（用于测试）
type MemoryStore struct {
	data map[string][]byte
	mu   sync.RWMutex
}

// NewMemoryStore 创建内存存储
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		data: make(map[string][]byte),
	}
}

// Get 获取值
func (s *MemoryStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, ok := s.data[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}

	// 返回副本
	result := make([]byte, len(data))
	copy(result, data)
	return result, nil
}

// Set 设置值
func (s *MemoryStore) Set(ctx context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 存储副本
	data := make([]byte, len(value))
	copy(data, value)
	s.data[key] = data
	return nil
}

// Delete 删除键
func (s *MemoryStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.data, key)
	return nil
}

// List 列出前缀匹配的键
func (s *MemoryStore) List(ctx context.Context, prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var keys []string
	for key := range s.data {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}

	return keys, nil
}

// Exists 检查键是否存在
func (s *MemoryStore) Exists(ctx context.Context, key string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, ok := s.data[key]
	return ok, nil
}

// Close 关闭存储
func (s *MemoryStore) Close() error {
	return nil
}

// Clear 清除所有数据
func (s *MemoryStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = make(map[string][]byte)
}

// Size 返回存储的键值对数量
func (s *MemoryStore) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}
