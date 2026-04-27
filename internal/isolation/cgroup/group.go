package cgroup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Limits struct {
	MemoryMaxBytes  int64
	PidsMax         int
	CPUQuotaMicros  int64
	CPUPeriodMicros int64
}

type Group struct {
	Path string
}

func ValidateParent(parentDir string, requiredControllers []string) error {
	return ValidateParentAt(parentDir, "/proc/self/mountinfo", requiredControllers)
}

func ValidateParentAt(parentDir, mountInfoPath string, requiredControllers []string) error {
	if strings.TrimSpace(parentDir) == "" {
		return fmt.Errorf("cgroup parent directory is required")
	}
	parentDir, err := filepath.Abs(parentDir)
	if err != nil {
		return fmt.Errorf("resolve cgroup parent: %w", err)
	}
	info, err := os.Stat(parentDir)
	if err != nil {
		return fmt.Errorf("stat cgroup parent: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cgroup parent is not a directory: %s", parentDir)
	}
	controllers, err := os.ReadFile(filepath.Join(parentDir, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("read cgroup.controllers: %w", err)
	}
	controllerSet := map[string]bool{}
	for _, controller := range strings.Fields(string(controllers)) {
		controllerSet[controller] = true
	}
	for _, controller := range requiredControllers {
		if !controllerSet[controller] {
			return fmt.Errorf("cgroup parent missing %s controller", controller)
		}
	}
	if _, err := os.Stat(filepath.Join(parentDir, "cgroup.subtree_control")); err != nil {
		return fmt.Errorf("stat cgroup.subtree_control: %w", err)
	}
	if err := validateCgroup2Mount(parentDir, mountInfoPath); err != nil {
		return err
	}
	return nil
}

func validateCgroup2Mount(parentDir, mountInfoPath string) error {
	mountInfo, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return fmt.Errorf("read mountinfo: %w", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(mountInfo)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] != "-" || fields[i+1] != "cgroup2" {
				continue
			}
			if len(fields) <= 4 {
				continue
			}
			mountPoint := unescapeMountInfoPath(fields[4])
			if parentDir == mountPoint || strings.HasPrefix(parentDir, strings.TrimRight(mountPoint, "/")+"/") {
				return nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan mountinfo: %w", err)
	}
	return fmt.Errorf("cgroup parent is not under a cgroup2 mount: %s", parentDir)
}

func unescapeMountInfoPath(path string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(path)
}

func RunName(prefix string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return '-'
		}
	}, strings.TrimSpace(prefix))
	clean = strings.Trim(clean, "-_")
	if clean == "" {
		clean = "run"
	}
	return fmt.Sprintf("%s-%d-%d", clean, os.Getpid(), time.Now().UnixNano())
}

func EnableControllers(parentDir string, controllers []string) error {
	if len(controllers) == 0 {
		return nil
	}
	values := make([]string, 0, len(controllers))
	for _, controller := range controllers {
		if controller == "" || strings.ContainsAny(controller, "+- /\t\r\n") {
			return fmt.Errorf("invalid cgroup controller: %q", controller)
		}
		values = append(values, "+"+controller)
	}
	if err := os.WriteFile(filepath.Join(parentDir, "cgroup.subtree_control"), []byte(strings.Join(values, " ")), 0o644); err != nil {
		return fmt.Errorf("enable cgroup controllers: %w", err)
	}
	return nil
}

func CreateRunGroup(parentDir, name string, limits Limits) (Group, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/ \t\r\n") {
		return Group{}, fmt.Errorf("invalid cgroup name: %q", name)
	}
	if limits.MemoryMaxBytes <= 0 {
		return Group{}, fmt.Errorf("memory cgroup limit must be positive")
	}
	if limits.PidsMax <= 0 {
		return Group{}, fmt.Errorf("pids cgroup limit must be positive")
	}
	if (limits.CPUQuotaMicros == 0) != (limits.CPUPeriodMicros == 0) {
		return Group{}, fmt.Errorf("cpu quota and period must be set together")
	}
	if limits.CPUQuotaMicros < 0 || limits.CPUPeriodMicros < 0 {
		return Group{}, fmt.Errorf("cpu quota and period must not be negative")
	}

	path := filepath.Join(parentDir, name)
	if err := os.Mkdir(path, 0o700); err != nil {
		return Group{}, fmt.Errorf("create cgroup %s: %w", path, err)
	}
	writes := []struct {
		file  string
		value string
	}{
		{file: "memory.max", value: strconv.FormatInt(limits.MemoryMaxBytes, 10)},
		{file: "memory.oom.group", value: "1"},
		{file: "pids.max", value: strconv.Itoa(limits.PidsMax)},
	}
	if limits.CPUQuotaMicros > 0 {
		writes = append(writes, struct {
			file  string
			value string
		}{file: "cpu.max", value: fmt.Sprintf("%d %d", limits.CPUQuotaMicros, limits.CPUPeriodMicros)})
	}
	for _, write := range writes {
		if err := os.WriteFile(filepath.Join(path, write.file), []byte(write.value), 0o644); err != nil {
			_ = os.RemoveAll(path)
			return Group{}, fmt.Errorf("write %s: %w", write.file, err)
		}
	}
	return Group{Path: path}, nil
}

func (g Group) AddProc(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("pid must be positive")
	}
	file, err := os.OpenFile(filepath.Join(g.Path, "cgroup.procs"), os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open cgroup.procs: %w", err)
	}
	defer file.Close()
	if _, err := fmt.Fprint(file, pid); err != nil {
		return fmt.Errorf("write cgroup.procs: %w", err)
	}
	return nil
}

func (g Group) Remove() error {
	if strings.TrimSpace(g.Path) == "" {
		return nil
	}
	if err := os.Remove(g.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove cgroup %s: %w", g.Path, err)
	}
	return nil
}
