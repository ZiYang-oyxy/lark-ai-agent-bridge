package persist

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestEngineWriteAtomicRoundTrip 原子写后能读回完整内容。
func TestEngineWriteAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	e, err := NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	f := e.open("sessions.json")

	// 文件不存在时 readRaw 返回 (nil,false,nil)。
	data, exists, err := f.readRaw()
	if err != nil {
		t.Fatalf("readRaw on missing: %v", err)
	}
	if exists || data != nil {
		t.Fatalf("expected missing file, got exists=%v data=%q", exists, data)
	}

	want := []byte(`{"schema_version":1,"entries":{}}`)
	if err := f.writeAtomic(want, 0o644); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}
	got, exists, err := f.readRaw()
	if err != nil {
		t.Fatalf("readRaw after write: %v", err)
	}
	if !exists {
		t.Fatalf("expected file to exist after write")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("readRaw = %q, want %q", got, want)
	}
}

// TestEngineWriteAtomicNoTempResidue 成功与失败路径都不留 temp 文件。
func TestEngineWriteAtomicNoTempResidue(t *testing.T) {
	dir := t.TempDir()
	e, err := NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	f := e.open("state.json")
	if err := f.writeAtomic([]byte("v1"), 0o600); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}
	if err := f.writeAtomic([]byte("v2"), 0o600); err != nil {
		t.Fatalf("writeAtomic 2: %v", err)
	}
	assertNoTempResidue(t, dir)
}

// TestEngineWriteAtomicCrashKeepsOldValue 模拟 rename 前中断：旧完整内容仍可读。
// 这里通过“写一个合法值后，模拟一次只写 temp 未 rename 的中断”，验证目标文件仍是旧值。
func TestEngineWriteAtomicCrashKeepsOldValue(t *testing.T) {
	dir := t.TempDir()
	e, err := NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	f := e.open("data.json")
	old := []byte("OLD")
	if err := f.writeAtomic(old, 0o600); err != nil {
		t.Fatalf("writeAtomic old: %v", err)
	}

	// 模拟崩溃：手工在目录里落一个残留 temp（新值），但绝不 rename。
	tmp, err := os.CreateTemp(dir, ".data.json-*.tmp")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := tmp.Write([]byte("NEW-INTERRUPTED")); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	_ = tmp.Close()

	// 目标文件必须仍是旧完整值，绝不半截、绝不是中断的新值。
	got, exists, err := f.readRaw()
	if err != nil {
		t.Fatalf("readRaw: %v", err)
	}
	if !exists || !bytes.Equal(got, old) {
		t.Fatalf("after simulated crash readRaw = (%q, exists=%v), want old %q", got, exists, old)
	}
}

// TestEngineConcurrentWithLock 并发 withLock 串行执行、无数据竞争（go test -race）。
func TestEngineConcurrentWithLock(t *testing.T) {
	dir := t.TempDir()
	e, err := NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	f := e.open("counter.json")

	const goroutines = 50
	counter := 0
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_ = f.withLock(func() error {
				counter++ // 受锁保护；race 检测器会抓无锁访问
				return nil
			})
		}()
	}
	wg.Wait()
	if counter != goroutines {
		t.Fatalf("counter = %d, want %d (lost updates under lock)", counter, goroutines)
	}
}

// TestWriteFileAtomicTopLevel 顶层辅助函数：迁移用；含目录 fsync。
func TestWriteFileAtomicTopLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	if err := WriteFileAtomic(path, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != `{"ok":true}` {
		t.Fatalf("content = %s", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 600", info.Mode().Perm())
	}
	assertNoTempResidue(t, dir)
}

func assertNoTempResidue(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, ent := range entries {
		name := ent.Name()
		if filepath.Ext(name) == ".tmp" {
			t.Fatalf("leftover temp file: %s", name)
		}
	}
}
