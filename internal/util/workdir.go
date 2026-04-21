package util

import (
	"fmt"
	"os"
	"strings"

	"aonohako/internal/platform"
)

func CreateWorkDir(prefix string) (string, error) {
	root := strings.TrimSpace(os.Getenv("AONOHAKO_WORK_ROOT"))
	if root == "" {
		if platform.UsesDedicatedWorkRoot() {
			return "", fmt.Errorf("AONOHAKO_WORK_ROOT is required in %s mode", platform.CurrentExecutionMode())
		}
		return os.MkdirTemp("", prefix)
	}
	if platform.UsesDedicatedWorkRoot() {
		info, err := os.Stat(root)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("AONOHAKO_WORK_ROOT is not a directory: %s", root)
		}
		return os.MkdirTemp(root, prefix)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, prefix)
}
