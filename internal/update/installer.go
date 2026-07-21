package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type StageFunc func(context.Context, Asset, string) (Staged, error)
type ExecFunc func(string, []string, []string) error

type Installer struct {
	Stage        StageFunc
	Executable   func() (string, error)
	Exec         ExecFunc
	Sleep        func(time.Duration)
	RestartDelay time.Duration
}

type Prepared struct {
	mu       sync.Mutex
	target   string
	staged   string
	previous string
	exec     ExecFunc
	sleep    func(time.Duration)
	delay    time.Duration
	replaced bool
	done     bool
}

func (i Installer) Prepare(ctx context.Context, asset Asset) (*Prepared, error) {
	if i.Stage == nil {
		return nil, errors.New("update installer has no staging function")
	}
	executable := i.Executable
	if executable == nil {
		executable = os.Executable
	}
	target, err := executable()
	if err != nil {
		return nil, fmt.Errorf("resolve current executable: %w", err)
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return nil, fmt.Errorf("resolve executable symlink: %w", err)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("stat current executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("current executable is not an executable regular file")
	}
	staged, err := i.Stage(ctx, asset, filepath.Dir(target))
	if err != nil {
		return nil, err
	}
	if filepath.Dir(staged.Path) != filepath.Dir(target) {
		_ = os.Remove(staged.Path)
		return nil, errors.New("staged update is not beside the current executable")
	}
	execFn := i.Exec
	if execFn == nil {
		execFn = syscall.Exec
	}
	sleep := i.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	delay := i.RestartDelay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	return &Prepared{
		target: target, staged: staged.Path, previous: target + ".previous",
		exec: execFn, sleep: sleep, delay: delay,
	}, nil
}

func (p *Prepared) Replace() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done || p.replaced {
		return errors.New("prepared update has already been used")
	}
	if p.target == "" || p.staged == "" {
		return errors.New("prepared update is incomplete")
	}
	if err := os.Chmod(p.staged, 0o755); err != nil {
		return fmt.Errorf("chmod staged update: %w", err)
	}
	if err := syncFile(p.staged); err != nil {
		return fmt.Errorf("sync staged update: %w", err)
	}
	dir := filepath.Dir(p.target)
	backup, err := os.CreateTemp(dir, "."+filepath.Base(p.target)+".previous-*")
	if err != nil {
		return fmt.Errorf("create previous binary: %w", err)
	}
	backupPath := backup.Name()
	cleanupBackup := true
	defer func() {
		_ = backup.Close()
		if cleanupBackup {
			_ = os.Remove(backupPath)
		}
	}()
	current, err := os.Open(p.target)
	if err != nil {
		return fmt.Errorf("open current binary: %w", err)
	}
	_, copyErr := io.Copy(backup, current)
	closeCurrentErr := current.Close()
	if copyErr != nil {
		return fmt.Errorf("copy previous binary: %w", copyErr)
	}
	if closeCurrentErr != nil {
		return fmt.Errorf("close current binary: %w", closeCurrentErr)
	}
	if err := backup.Chmod(0o755); err != nil {
		return fmt.Errorf("chmod previous binary: %w", err)
	}
	if err := backup.Sync(); err != nil {
		return fmt.Errorf("sync previous binary: %w", err)
	}
	if err := backup.Close(); err != nil {
		return fmt.Errorf("close previous binary: %w", err)
	}
	if err := os.Rename(backupPath, p.previous); err != nil {
		return fmt.Errorf("publish previous binary: %w", err)
	}
	cleanupBackup = false
	if err := os.Rename(p.staged, p.target); err != nil {
		return fmt.Errorf("replace current binary: %w", err)
	}
	if err := syncDir(dir); err != nil {
		rollbackErr := os.Rename(p.previous, p.target)
		return errors.Join(fmt.Errorf("sync replaced binary directory: %w", err), rollbackErr)
	}
	p.staged = ""
	p.replaced = true
	return nil
}

func (p *Prepared) Restart(argv, env []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done || !p.replaced {
		return errors.New("prepared update has not been replaced or was already used")
	}
	p.sleep(p.delay)
	execErr := p.exec(p.target, argv, env)
	if execErr == nil {
		p.done = true
		return nil
	}
	rollbackErr := os.Rename(p.previous, p.target)
	if rollbackErr == nil {
		rollbackErr = syncDir(filepath.Dir(p.target))
	}
	p.done = true
	p.replaced = false
	if rollbackErr != nil {
		return errors.Join(execErr, fmt.Errorf("rollback previous binary: %w", rollbackErr))
	}
	return execErr
}

func (p *Prepared) Abort() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done || p.replaced {
		return
	}
	if p.staged != "" {
		_ = os.Remove(p.staged)
		p.staged = ""
	}
	p.done = true
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
