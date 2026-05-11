package compile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/security"
	"aonohako/internal/util"
)

func materializeSources(root string, sources []model.Source) error {
	totalBytes := 0
	for _, src := range sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return err
		}
		data, err := util.DecodeB64(src.DataB64)
		if err != nil {
			return fmt.Errorf("decode %s: %w", clean, err)
		}
		if len(data) > maxDecodedSourceBytes {
			return fmt.Errorf("source too large: %s", clean)
		}
		totalBytes += len(data)
		if totalBytes > maxDecodedSourceTotalBytes {
			return fmt.Errorf("sources total size exceeded")
		}
		dest := filepath.Join(root, clean)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", clean, err)
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", clean, err)
		}
	}
	return nil
}

func hardenCompileWorkspace(workDir string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	const sandboxUID = 65532
	const sandboxGID = 65532
	scopedDirs := make(map[string]struct{}, len(security.WorkspaceScopedDirs(workDir)))
	for _, dir := range security.WorkspaceScopedDirs(workDir) {
		scopedDirs[dir] = struct{}{}
	}
	if err := filepath.WalkDir(workDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != workDir {
			if _, ok := scopedDirs[path]; ok {
				return filepath.SkipDir
			}
		}
		if d.IsDir() {
			return os.Chmod(path, 0o777|os.ModeSticky)
		}
		return os.Chmod(path, 0o444)
	}); err != nil {
		return err
	}
	for _, dir := range security.WorkspaceScopedDirs(workDir) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chown(dir, sandboxUID, sandboxGID); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func validateTargetName(raw string) (string, error) {
	clean, err := util.ValidateRelativePath(raw)
	if err != nil {
		return "", err
	}
	if filepath.Base(clean) != clean || strings.ContainsAny(clean, `/\`) {
		return "", fmt.Errorf("invalid target: %q", raw)
	}
	return clean, nil
}
