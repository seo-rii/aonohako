package security

import (
	"fmt"
	"os"
	"path/filepath"
)

const dotnetSandboxUID = 65532

func ResetDotnetSharedState() error {
	if os.Geteuid() != 0 {
		return nil
	}
	root := "/tmp/.dotnet"
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	for _, dir := range []string{
		root,
		filepath.Join(root, "shm"),
		filepath.Join(root, "shm", "global"),
		filepath.Join(root, "lockfiles"),
		filepath.Join(root, "lockfiles", "global"),
	} {
		if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		if err := os.Chown(dir, dotnetSandboxUID, dotnetSandboxUID); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("unsafe dotnet state directory: %s", dir)
		}
	}
	return nil
}
