// Package persist 提供 bridge 的统一文件持久化底座与两个门面。
//
// 底座 Engine 是全框架唯一的落盘实现，保证同步 durable 原子写
// （temp → fsync(temp) → rename → fsync(dir)）：任一 writeAtomic 返回成功即已落盘，
// 进程随后崩溃也只会读到旧完整内容或新完整内容，绝无半截。
//
// 承载于底座之上的两个门面：泛型 Store[T]（按 key 状态仓，见 store.go）与
// 强类型 Config（分层配置，见 config.go / layers.go / env.go）。
package persist

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Engine 是全框架唯一的落盘实现。每个持久化对象（配置、每个状态 store）持有一个 file。
type Engine struct {
	dir string // <workdir>/.lark-agent-bridge
}

// NewEngine 创建/确保数据目录存在并返回引擎。
func NewEngine(dir string) (*Engine, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("persist: empty engine dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("persist: create engine dir: %w", err)
	}
	return &Engine{dir: dir}, nil
}

// Dir 返回引擎数据目录。
func (e *Engine) Dir() string { return e.dir }

// file 代表一个 JSON 文件的原子读写单元，自带一把锁。
type file struct {
	path string
	mu   sync.Mutex
}

// open 返回指定文件名（如 "sessions.json"）的读写单元。
//
// 每次调用返回一个新的 file 句柄，各自持有独立锁；因此同一逻辑文件应只 open 一次并复用
// 该句柄（Store/Config 门面各自缓存自己的 file），否则并发保证失效。
func (e *Engine) open(name string) *file {
	return &file{path: filepath.Join(e.dir, name)}
}

// readRaw 读原始字节；文件不存在返回 (nil, false, nil)；解析交给上层门面。
func (f *file) readRaw() (data []byte, exists bool, err error) {
	raw, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("persist: read %s: %w", filepath.Base(f.path), err)
	}
	return raw, true, nil
}

// writeAtomic 同步 durable 写：temp → fsync(temp) → rename → fsync(dir)。成功返回即已落盘。
//
// perm 传 0600（配置，含 secret 相邻数据）或 0644（普通状态）。
// 任一步失败返回 error 且不留半截文件（temp 清理），目标文件保持写前状态。
func (f *file) writeAtomic(data []byte, perm os.FileMode) error {
	dir := filepath.Dir(f.path)
	base := filepath.Base(f.path)

	tmp, err := os.CreateTemp(dir, "."+base+"-*.tmp")
	if err != nil {
		return fmt.Errorf("persist: create temp for %s: %w", base, err)
	}
	tmpName := tmp.Name()
	// 无论成功失败都清理 temp：成功时 rename 已把它移走（Remove 变 no-op），
	// 失败时确保不残留半截 temp。
	defer os.Remove(tmpName)

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("persist: chmod temp for %s: %w", base, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("persist: write temp for %s: %w", base, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("persist: fsync temp for %s: %w", base, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("persist: close temp for %s: %w", base, err)
	}
	if err := os.Rename(tmpName, f.path); err != nil {
		return fmt.Errorf("persist: rename temp for %s: %w", base, err)
	}
	// 目录 fsync：确保 rename 这条目录项变更本身落盘，
	// 否则崩溃后可能出现“文件内容在但目录项丢”的窗口。
	if err := fsyncDir(dir); err != nil {
		return fmt.Errorf("persist: fsync dir for %s: %w", base, err)
	}
	return nil
}

// withLock 在文件锁内执行 fn（供 Store.Update 的原子读改写复用）。
func (f *file) withLock(fn func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fn()
}

// fsyncDir 打开目录并 fsync，使新增/改名的目录项持久化。
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return err
	}
	return nil
}
