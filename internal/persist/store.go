package persist

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// errSentinel 供测试触发 Update 回滚路径使用。
var errSentinel = errors.New("persist: sentinel")

// MigrateFn 把旧版本的原始 entries JSON 升级到当前版本。首版可为 nil。
// raw 是磁盘文档里 entries 字段的原始字节；返回升级后的 entries 字节。
type MigrateFn func(oldVer int, raw []byte) (newRaw []byte, err error)

// storeDoc 是 Store 的磁盘文档：{"schema_version":<ver>,"entries":{"<key>":<T>}}。
type storeDoc struct {
	SchemaVersion int             `json:"schema_version"`
	Entries       json.RawMessage `json:"entries"`
}

// Store 是按 key 的持久化记录仓，内存态 + 单文件落盘。
type Store[T any] struct {
	file    *file
	ver     int
	entries map[string]T
}

// Open 打开/创建一个 store。name 如 "sessions.json"；ver 是当前 schema 版本。
//
// 文件损坏、或版本低于 ver 且无法迁移（migrate==nil），返回 error，
// 上层据此决定是否 --reset-store。
func Open[T any](e *Engine, name string, ver int, migrate MigrateFn) (*Store[T], error) {
	if e == nil {
		return nil, errors.New("persist: nil engine")
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("persist: empty store name")
	}
	s := &Store[T]{file: e.open(name), ver: ver, entries: map[string]T{}}

	raw, exists, err := s.file.readRaw()
	if err != nil {
		return nil, err
	}
	if !exists {
		return s, nil
	}

	var doc storeDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("persist: decode %s: %w", name, err)
	}

	entriesRaw := doc.Entries
	if doc.SchemaVersion != ver {
		if doc.SchemaVersion > ver {
			return nil, fmt.Errorf("persist: %s schema %d newer than supported %d", name, doc.SchemaVersion, ver)
		}
		if migrate == nil {
			return nil, fmt.Errorf("persist: %s schema %d != %d and no migration", name, doc.SchemaVersion, ver)
		}
		migrated, err := migrate(doc.SchemaVersion, entriesRaw)
		if err != nil {
			return nil, fmt.Errorf("persist: migrate %s from %d: %w", name, doc.SchemaVersion, err)
		}
		entriesRaw = migrated
	}

	if len(entriesRaw) > 0 {
		if err := json.Unmarshal(entriesRaw, &s.entries); err != nil {
			return nil, fmt.Errorf("persist: decode %s entries: %w", name, err)
		}
	}
	if s.entries == nil {
		s.entries = map[string]T{}
	}
	return s, nil
}

// Get 读取一个 key（存在返回 (v,true)，否则零值 false）。
func (s *Store[T]) Get(key string) (T, bool) {
	var out T
	if err := s.file.withLock(func() error {
		if v, ok := s.entries[key]; ok {
			out = v
			return nil
		}
		return errNotFound
	}); err != nil {
		var zero T
		return zero, false
	}
	return out, true
}

var errNotFound = errors.New("persist: not found")

// Put 写入一个 key（同步 durable）。
func (s *Store[T]) Put(key string, v T) error {
	return s.file.withLock(func() error {
		next := s.clone()
		next[key] = v
		if err := s.flush(next); err != nil {
			return err
		}
		s.entries = next
		return nil
	})
}

// Delete 删除一个 key（同步 durable）。
func (s *Store[T]) Delete(key string) error {
	return s.file.withLock(func() error {
		if _, ok := s.entries[key]; !ok {
			return nil
		}
		next := s.clone()
		delete(next, key)
		if err := s.flush(next); err != nil {
			return err
		}
		s.entries = next
		return nil
	})
}

// Range 遍历所有条目（在锁内，fn 返回 false 提前停止）。
func (s *Store[T]) Range(fn func(key string, v T) bool) {
	_ = s.file.withLock(func() error {
		for k, v := range s.entries {
			if !fn(k, v) {
				break
			}
		}
		return nil
	})
}

// Snapshot 返回内存态的深拷贝，供只读遍历，改它不影响 store 内部。
func (s *Store[T]) Snapshot() map[string]T {
	var out map[string]T
	_ = s.file.withLock(func() error {
		out = s.clone()
		return nil
	})
	return out
}

// Update 在文件锁内：读当前值（不存在则零值）→ 调 fn 改 → 落盘 → 更新内存。
//
// fn 返回 error 则整体回滚、不落盘、不改内存。同 key 并发 Update 无丢更新。
// 仅高频/强一致 store 使用（session 队列 / action-grant / native-sequence）。
func (s *Store[T]) Update(key string, fn func(*T) error) error {
	return s.file.withLock(func() error {
		cur := s.entries[key] // 不存在得零值
		if err := fn(&cur); err != nil {
			return err
		}
		next := s.clone()
		next[key] = cur
		if err := s.flush(next); err != nil {
			return err
		}
		s.entries = next
		return nil
	})
}

// clone 深拷贝内存 map（T 为值类型，浅层复制即可隔离外部对 map 的改动）。
func (s *Store[T]) clone() map[string]T {
	out := make(map[string]T, len(s.entries))
	for k, v := range s.entries {
		out[k] = v
	}
	return out
}

// flush 把 entries 序列化为磁盘文档并同步 durable 写。调用方须已持有文件锁。
func (s *Store[T]) flush(entries map[string]T) error {
	entriesRaw, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("persist: encode %s entries: %w", filepath.Base(s.file.path), err)
	}
	doc := storeDoc{SchemaVersion: s.ver, Entries: entriesRaw}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("persist: encode %s: %w", filepath.Base(s.file.path), err)
	}
	data = append(data, '\n')
	return s.file.writeAtomic(data, 0o644)
}
