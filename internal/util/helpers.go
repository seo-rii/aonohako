package util

import (
	"encoding/base64"
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
		"PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
}
