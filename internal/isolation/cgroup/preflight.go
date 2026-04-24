package cgroup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type PreflightResult struct {
	Available   bool
	Root        string
	Controllers []string
	SupportsIO  bool
	Missing     []string
	Reason      string
}

func Preflight() PreflightResult {
	return PreflightAt("/sys/fs/cgroup", "/proc/self/mountinfo")
}

func PreflightAt(root, mountInfoPath string) PreflightResult {
	result := PreflightResult{Root: root}
	mountInfo, err := os.ReadFile(mountInfoPath)
	if err != nil {
		result.Reason = fmt.Sprintf("read mountinfo: %v", err)
		return result
	}
	mounted := false
	scanner := bufio.NewScanner(strings.NewReader(string(mountInfo)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] == "-" && fields[i+1] == "cgroup2" {
				if len(fields) > 4 && fields[4] == root {
					mounted = true
					break
				}
			}
		}
		if mounted {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		result.Reason = fmt.Sprintf("scan mountinfo: %v", err)
		return result
	}
	if !mounted {
		result.Missing = append(result.Missing, "mount:cgroup2")
		result.Reason = "cgroup v2 mount not found"
		return result
	}

	controllerBody, err := os.ReadFile(filepath.Join(root, "cgroup.controllers"))
	if err != nil {
		result.Missing = append(result.Missing, "file:cgroup.controllers")
		result.Reason = fmt.Sprintf("read cgroup.controllers: %v", err)
		return result
	}
	controllerSet := map[string]bool{}
	for _, controller := range strings.Fields(string(controllerBody)) {
		controllerSet[controller] = true
		result.Controllers = append(result.Controllers, controller)
	}
	sort.Strings(result.Controllers)
	for _, controller := range []string{"cpu", "memory", "pids"} {
		if !controllerSet[controller] {
			result.Missing = append(result.Missing, "controller:"+controller)
		}
	}
	result.SupportsIO = controllerSet["io"]

	if _, err := os.Stat(filepath.Join(root, "cgroup.subtree_control")); err != nil {
		result.Missing = append(result.Missing, "file:cgroup.subtree_control")
	}
	if len(result.Missing) > 0 {
		result.Reason = "required cgroup v2 controls are unavailable"
		return result
	}
	result.Available = true
	return result
}
