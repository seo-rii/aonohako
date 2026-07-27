package security

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	dotnetSharedStatePath = "/tmp/.dotnet"
	konanCacheLockPath    = "/usr/local/lib/aonohako/konan/cache/.lock"
)

var runtimeStateMu sync.RWMutex

type RuntimeStateLease struct {
	once    sync.Once
	release func() error
	err     error
}

func (l *RuntimeStateLease) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.release != nil {
			l.err = l.release()
		}
	})
	return l.err
}

// AcquireRuntimeState prevents ordinary sandbox commands from overlapping a
// runtime that temporarily exposes a fixed compatibility path. Production
// runners already enforce one active run, while this lease makes accidental
// local concurrency fail closed instead of exposing the path to the shared
// sandbox identity.
func AcquireRuntimeState(workDir, commandBase string, uid, gid int) (*RuntimeStateLease, error) {
	return acquireRuntimeStateAt(
		workDir,
		commandBase,
		uid,
		gid,
		os.Geteuid() == 0,
		dotnetSharedStatePath,
		konanCacheLockPath,
	)
}

func acquireRuntimeStateAt(workDir, commandBase string, uid, gid int, manageFilesystem bool, dotnetPath, konanLockPath string) (*RuntimeStateLease, error) {
	exclusive := commandBase == "dotnet" || commandBase == "dafny" || commandBase == "kotlinc-native"
	if exclusive {
		if !runtimeStateMu.TryLock() {
			return nil, fmt.Errorf("shared runtime state is busy")
		}
	} else if !runtimeStateMu.TryRLock() {
		return nil, fmt.Errorf("shared runtime state is busy")
	}

	unlock := func() {
		if exclusive {
			runtimeStateMu.Unlock()
		} else {
			runtimeStateMu.RUnlock()
		}
	}
	if !manageFilesystem || !exclusive {
		return &RuntimeStateLease{release: func() error {
			unlock()
			return nil
		}}, nil
	}

	var cleanup func() error
	var err error
	switch commandBase {
	case "dotnet", "dafny":
		cleanup, err = prepareDotnetSharedState(workDir, uid, gid, dotnetPath)
	case "kotlinc-native":
		cleanup, err = prepareKonanCacheLock(workDir, uid, gid, konanLockPath)
	}
	if err != nil {
		unlock()
		return nil, err
	}
	return &RuntimeStateLease{release: func() error {
		err := cleanup()
		unlock()
		return err
	}}, nil
}

func prepareDotnetSharedState(workDir string, uid, gid int, globalPath string) (func() error, error) {
	target := filepath.Join(workDir, ".dotnet-shared")
	for _, dir := range []string{
		target,
		filepath.Join(target, "shm"),
		filepath.Join(target, "shm", "global"),
		filepath.Join(target, "lockfiles"),
		filepath.Join(target, "lockfiles", "global"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := os.Chown(dir, uid, gid); err != nil {
			return nil, err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if err := replaceWithSymlink(globalPath, target); err != nil {
		return nil, err
	}
	return func() error {
		if err := removeExpectedSymlink(globalPath, target); err != nil {
			return err
		}
		return sealDirectory(globalPath)
	}, nil
}

func prepareKonanCacheLock(workDir string, uid, gid int, globalPath string) (func() error, error) {
	target := filepath.Join(workDir, ".konan-cache-lock")
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chown(uid, gid); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := replaceWithSymlink(globalPath, target); err != nil {
		return nil, err
	}
	return func() error {
		if err := removeExpectedSymlink(globalPath, target); err != nil {
			return err
		}
		return sealFile(globalPath)
	}, nil
}

func replaceWithSymlink(linkPath, target string) error {
	if err := os.RemoveAll(linkPath); err != nil {
		return err
	}
	if err := os.Symlink(target, linkPath); err != nil {
		return err
	}
	info, err := os.Lstat(linkPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("runtime state path is not a symlink: %s", linkPath)
	}
	return nil
}

func removeExpectedSymlink(linkPath, target string) error {
	info, err := os.Lstat(linkPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("runtime state path was replaced: %s", linkPath)
	}
	actual, err := os.Readlink(linkPath)
	if err != nil {
		return err
	}
	if actual != target {
		return fmt.Errorf("runtime state path target changed: %s", linkPath)
	}
	return os.Remove(linkPath)
}

func sealDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	if err := os.Chown(path, os.Geteuid(), os.Getegid()); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func sealFile(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400)
	if err != nil {
		return err
	}
	if err := file.Chown(os.Geteuid(), os.Getegid()); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
