package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"aonohako/internal/config"
)

func newRuntimeReadinessCheck(cfg config.Config) func() error {
	contract, contractErr := cfg.Execution.Platform.SecurityContract()
	workRoot := strings.TrimSpace(os.Getenv("AONOHAKO_WORK_ROOT"))
	var workRootDevice uint64
	if info, err := os.Stat(workRoot); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			workRootDevice = uint64(stat.Dev)
		}
	}
	cgroupParent := strings.TrimSpace(cfg.Execution.Cgroup.ParentDir)
	var cgroupDevice uint64
	if info, err := os.Stat(cgroupParent); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			cgroupDevice = uint64(stat.Dev)
		}
	}

	return func() error {
		if contractErr != nil {
			return contractErr
		}
		if contract.RequiresDedicatedWorkRoot {
			if workRoot == "" {
				return fmt.Errorf("work root is not configured")
			}
			info, err := os.Stat(workRoot)
			if err != nil {
				return fmt.Errorf("stat work root: %w", err)
			}
			if !info.IsDir() {
				return fmt.Errorf("work root is not a directory")
			}
			if info.Mode().Perm()&0o022 != 0 {
				return fmt.Errorf("work root became group/world writable")
			}
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				if int(stat.Uid) != os.Geteuid() {
					return fmt.Errorf("work root ownership changed")
				}
				if workRootDevice != 0 && uint64(stat.Dev) != workRootDevice {
					return fmt.Errorf("work root mount changed")
				}
			}
			if cfg.WorkRootMaxBytes > 0 || cfg.WorkRootMaxFiles > 0 {
				var fs syscall.Statfs_t
				if err := syscall.Statfs(workRoot, &fs); err != nil {
					return fmt.Errorf("stat work root filesystem: %w", err)
				}
				if cfg.WorkRootMaxBytes > 0 {
					if fs.Bsize <= 0 || fs.Blocks*uint64(fs.Bsize) > uint64(cfg.WorkRootMaxBytes) {
						return fmt.Errorf("work root filesystem exceeds byte limit")
					}
				}
				if cfg.WorkRootMaxFiles > 0 && (fs.Files == 0 || fs.Files > uint64(cfg.WorkRootMaxFiles)) {
					return fmt.Errorf("work root filesystem exceeds inode limit")
				}
			}
		}

		if cgroupParent == "" {
			return nil
		}
		info, err := os.Stat(cgroupParent)
		if err != nil {
			return fmt.Errorf("stat cgroup parent: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("cgroup parent is not a directory")
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && cgroupDevice != 0 && uint64(stat.Dev) != cgroupDevice {
			return fmt.Errorf("cgroup parent mount changed")
		}
		controllers, err := os.ReadFile(filepath.Join(cgroupParent, "cgroup.controllers"))
		if err != nil {
			return fmt.Errorf("read cgroup controllers: %w", err)
		}
		available := make(map[string]bool)
		for _, controller := range strings.Fields(string(controllers)) {
			available[controller] = true
		}
		for _, required := range []string{"cpu", "memory", "pids"} {
			if !available[required] {
				return fmt.Errorf("cgroup parent lost %s controller", required)
			}
		}
		if _, err := os.Stat(filepath.Join(cgroupParent, "cgroup.subtree_control")); err != nil {
			return fmt.Errorf("stat cgroup subtree control: %w", err)
		}
		procs, err := os.ReadFile(filepath.Join(cgroupParent, "cgroup.procs"))
		if err != nil {
			return fmt.Errorf("read cgroup parent processes: %w", err)
		}
		if strings.TrimSpace(string(procs)) != "" {
			return fmt.Errorf("cgroup parent contains processes")
		}
		return nil
	}
}
