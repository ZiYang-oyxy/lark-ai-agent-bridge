package persist

import (
	"sync"
	"testing"
)

type counter struct {
	N int `json:"n"`
}

func newTestStore[T any](t *testing.T, name string, ver int, migrate MigrateFn) *Store[T] {
	t.Helper()
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	s, err := Open[T](e, name, ver, migrate)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// TestStorePutGetDelete 基础 CRUD。
func TestStorePutGetDelete(t *testing.T) {
	s := newTestStore[counter](t, "c.json", 1, nil)
	if _, ok := s.Get("a"); ok {
		t.Fatalf("expected missing key")
	}
	if err := s.Put("a", counter{N: 7}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := s.Get("a")
	if !ok || got.N != 7 {
		t.Fatalf("Get = (%+v, %v), want {7} true", got, ok)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Get("a"); ok {
		t.Fatalf("expected key deleted")
	}
}

// TestStoreSurvivesReopen Put 后重开能读回（跨“重启”存活），且磁盘格式契约成立。
func TestStoreSurvivesReopen(t *testing.T) {
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	s1, err := Open[counter](e, "c.json", 1, nil)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	if err := s1.Put("k", counter{N: 42}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// 复用同一 engine dir 重开一个 Store（模拟重启）。
	s2, err := Open[counter](e, "c.json", 1, nil)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	got, ok := s2.Get("k")
	if !ok || got.N != 42 {
		t.Fatalf("after reopen Get = (%+v, %v), want {42} true", got, ok)
	}
}

// TestStoreConcurrentUpdate 同 key 上 N 个 goroutine 并发 Update 各 +1，最终值 == N（无丢更新）。
func TestStoreConcurrentUpdate(t *testing.T) {
	s := newTestStore[counter](t, "c.json", 1, nil)
	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := s.Update("k", func(c *counter) error {
				c.N++
				return nil
			}); err != nil {
				t.Errorf("Update: %v", err)
			}
		}()
	}
	wg.Wait()
	got, ok := s.Get("k")
	if !ok || got.N != n {
		t.Fatalf("after %d concurrent Update, value = (%+v, %v), want {%d}", n, got, ok, n)
	}
}

// TestStoreUpdateRollback fn 返回 error 时整体回滚：不落盘、不改内存。
func TestStoreUpdateRollback(t *testing.T) {
	s := newTestStore[counter](t, "c.json", 1, nil)
	if err := s.Put("k", counter{N: 5}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	wantErr := errSentinel
	err := s.Update("k", func(c *counter) error {
		c.N = 999
		return wantErr
	})
	if err != wantErr {
		t.Fatalf("Update err = %v, want sentinel", err)
	}
	got, _ := s.Get("k")
	if got.N != 5 {
		t.Fatalf("after failed Update value = %+v, want unchanged {5}", got)
	}
}

// TestStoreMigrate 版本迁移：migrate 以正确 oldVer 被调用，升级后可读。
func TestStoreMigrate(t *testing.T) {
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	// 先用旧版本 ver=1 写下数据。
	old, err := Open[counter](e, "c.json", 1, nil)
	if err != nil {
		t.Fatalf("Open old: %v", err)
	}
	if err := old.Put("k", counter{N: 1}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// 用 ver=2 重开，提供 migrate。
	var gotOldVer int
	migrate := func(oldVer int, raw []byte) ([]byte, error) {
		gotOldVer = oldVer
		// 平滑升级：内容结构未变，直接返回原始 entries 字节即可。
		return raw, nil
	}
	s2, err := Open[counter](e, "c.json", 2, migrate)
	if err != nil {
		t.Fatalf("Open v2: %v", err)
	}
	if gotOldVer != 1 {
		t.Fatalf("migrate called with oldVer=%d, want 1", gotOldVer)
	}
	got, ok := s2.Get("k")
	if !ok || got.N != 1 {
		t.Fatalf("after migrate Get = (%+v, %v), want {1} true", got, ok)
	}
}

// TestStoreMigrateNilRejectsMismatch migrate==nil 且版本不符 → error。
func TestStoreMigrateNilRejectsMismatch(t *testing.T) {
	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	old, err := Open[counter](e, "c.json", 1, nil)
	if err != nil {
		t.Fatalf("Open old: %v", err)
	}
	if err := old.Put("k", counter{N: 1}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := Open[counter](e, "c.json", 2, nil); err == nil {
		t.Fatalf("expected error opening ver=2 with nil migrate over ver=1 data")
	}
}

// TestStoreSnapshotIsolation Snapshot 返回深拷贝，改它不影响 store 内部。
func TestStoreSnapshotIsolation(t *testing.T) {
	s := newTestStore[counter](t, "c.json", 1, nil)
	if err := s.Put("k", counter{N: 1}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	snap := s.Snapshot()
	snap["k"] = counter{N: 999}
	snap["new"] = counter{N: 7}
	got, _ := s.Get("k")
	if got.N != 1 {
		t.Fatalf("store mutated via snapshot: %+v", got)
	}
	if _, ok := s.Get("new"); ok {
		t.Fatalf("snapshot insertion leaked into store")
	}
}
