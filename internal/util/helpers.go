package util

import (
	"encoding/base64"
	"errors"
	"os/exec"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func DecodeB64(raw string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(raw)
}

func EncodeB64(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}

func ValidateRelativePath(name string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(name))
	if clean == "" || clean == "." || strings.Contains(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("invalid path: %q", name)
	}
	return clean, nil
}

func MaterializeBase64Files(root string, files map[string]string, mode os.FileMode) error {
	for name, b64 := range files {
		clean, err := ValidateRelativePath(name)
		if err != nil {
			return err
		}
		data, err := DecodeB64(b64)
		if err != nil {
			return fmt.Errorf("decode %s: %w", clean, err)
		}
		dest := filepath.Join(root, clean)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", clean, err)
		}
		if err := os.WriteFile(dest, data, mode); err != nil {
			return fmt.Errorf("write %s: %w", clean, err)
		}
	}
	return nil
}

func BaseEnv() []string {
	return []string{
		"PATH=/usr/local/go/bin:/usr/local/cargo/bin:/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"PYTHONPATH=/usr/local/lib/aonohako/python",
		"PYTHONDONTWRITEBYTECODE=1",
		"RUSTUP_HOME=/usr/local/rustup",
		"CARGO_HOME=/usr/local/cargo",
		"DOTNET_ROOT=/opt/dotnet",
	}
}

func ResolveCommandPath(name string, env []string) (string, error) {
	resolvePath := func(path string) (string, error) {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("not executable: %s", path)
		}
		if real, err := filepath.EvalSymlinks(path); err == nil && real != "" {
			path = real
		}
		return path, nil
	}

	if filepath.IsAbs(name) || strings.ContainsRune(name, filepath.Separator) {
		return resolvePath(name)
	}

	pathEnv := ""
	for _, item := range env {
		if strings.HasPrefix(item, "PATH=") {
			pathEnv = strings.TrimPrefix(item, "PATH=")
			break
		}
	}
	if pathEnv == "" {
		pathEnv = "/usr/local/go/bin:/usr/local/cargo/bin:/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin"
	}

	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		path, err := resolvePath(filepath.Join(dir, name))
		if err == nil {
			return path, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
	}
	return "", exec.ErrNotFound
}
