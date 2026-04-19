package util

import (
	"os"
	"strings"
)

func CreateWorkDir(prefix string) (string, error) {
	root := strings.TrimSpace(getenvAny([]string{"AONOHAKO_WORK_ROOT", "GO_WORK_ROOT"}))
	if root == "" {
		return os.MkdirTemp("", prefix)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, prefix)
}

func getenvAny(keys []string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
