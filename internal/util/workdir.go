package util

import (
	"os"
	"strings"
)

func CreateWorkDir(prefix string) (string, error) {
	root := strings.TrimSpace(os.Getenv("AONOHAKO_WORK_ROOT"))
	if root == "" {
		return os.MkdirTemp("", prefix)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, prefix)
}
