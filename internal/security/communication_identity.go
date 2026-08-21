package security

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	CommunicationParticipantUIDMin uint32 = 64937
	CommunicationParticipantUIDMax uint32 = 65000
	CommunicationParticipantGIDMin uint32 = CommunicationParticipantUIDMin
	CommunicationParticipantGIDMax uint32 = CommunicationParticipantUIDMax
	CommunicationManagerUID        uint32 = 65531
	CommunicationManagerGID        uint32 = CommunicationManagerUID
	CommunicationParticipantSlots         = int(CommunicationParticipantUIDMax-CommunicationParticipantUIDMin) + 1
)

type CommunicationIdentityOwnedPath struct {
	Path string
	UID  uint32
	GID  uint32
}

func CommunicationParticipantUIDForIndex(index int) (uint32, bool) {
	if index < 0 || index >= CommunicationParticipantSlots {
		return 0, false
	}
	return CommunicationParticipantUIDMax - uint32(index), true
}

func IsReservedCommunicationIdentity(id uint32) bool {
	return id == CommunicationManagerUID ||
		id >= CommunicationParticipantUIDMin && id <= CommunicationParticipantUIDMax
}

func ValidateCommunicationIdentityReservation() error {
	if err := ValidateCommunicationIdentityReservationAt("/etc/passwd", "/etc/group", "/proc"); err != nil {
		return err
	}
	owned, err := CommunicationIdentityOwnedImagePaths("/")
	if err != nil {
		return fmt.Errorf("scan image ownership for reserved communication identities: %w", err)
	}
	if len(owned) == 0 {
		return nil
	}
	shown := make([]string, 0, min(len(owned), 8))
	for _, item := range owned[:min(len(owned), 8)] {
		shown = append(shown, fmt.Sprintf("%s(uid=%d,gid=%d)", item.Path, item.UID, item.GID))
	}
	suffix := ""
	if remaining := len(owned) - len(shown); remaining > 0 {
		suffix = fmt.Sprintf(" and %d more", remaining)
	}
	return fmt.Errorf("reserved communication identity owns image paths: %s%s", strings.Join(shown, ", "), suffix)
}

func ValidateCommunicationIdentityReservationAt(passwdPath, groupPath, procRoot string) error {
	if err := validateCommunicationPasswd(passwdPath); err != nil {
		return err
	}
	if err := validateCommunicationGroup(groupPath); err != nil {
		return err
	}
	return validateCommunicationProcesses(procRoot)
}

func validateCommunicationPasswd(path string) error {
	return scanIdentityDatabase(path, 4, func(fields []string, line int) error {
		uid, err := parseIdentityField(path, fields[2], line, "uid")
		if err != nil {
			return err
		}
		gid, err := parseIdentityField(path, fields[3], line, "gid")
		if err != nil {
			return err
		}
		if IsReservedCommunicationIdentity(uid) {
			return fmt.Errorf("reserved communication uid %d is assigned to passwd entry %q", uid, fields[0])
		}
		if IsReservedCommunicationIdentity(gid) {
			return fmt.Errorf("reserved communication gid %d is assigned to passwd entry %q", gid, fields[0])
		}
		return nil
	})
}

func validateCommunicationGroup(path string) error {
	return scanIdentityDatabase(path, 3, func(fields []string, line int) error {
		gid, err := parseIdentityField(path, fields[2], line, "gid")
		if err != nil {
			return err
		}
		if IsReservedCommunicationIdentity(gid) {
			return fmt.Errorf("reserved communication gid %d is assigned to group entry %q", gid, fields[0])
		}
		return nil
	})
}

func scanIdentityDatabase(path string, minimumFields int, check func([]string, int) error) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, ":")
		if len(fields) < minimumFields {
			return fmt.Errorf("parse %s line %d: expected at least %d fields", path, line, minimumFields)
		}
		if err := check(fields, line); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan %s: %w", path, err)
	}
	return nil
}

func parseIdentityField(path, raw string, line int, field string) (uint32, error) {
	value, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse %s line %d %s: %w", path, line, field, err)
	}
	return uint32(value), nil
}

func validateCommunicationProcesses(procRoot string) error {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return fmt.Errorf("read proc root %s: %w", procRoot, err)
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		statusPath := filepath.Join(procRoot, entry.Name(), "status")
		body, err := os.ReadFile(statusPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read process %d status: %w", pid, err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[0] != "Uid:" && fields[0] != "Gid:" && fields[0] != "Groups:" {
				continue
			}
			for _, raw := range fields[1:] {
				id, err := strconv.ParseUint(raw, 10, 32)
				if err != nil {
					return fmt.Errorf("parse process %d %s identity %q: %w", pid, fields[0], raw, err)
				}
				if IsReservedCommunicationIdentity(uint32(id)) {
					return fmt.Errorf("reserved communication identity %d is in use by process %d (%s)", id, pid, strings.TrimSuffix(fields[0], ":"))
				}
			}
		}
	}
	return nil
}

func CommunicationIdentityOwnedImagePaths(root string) ([]CommunicationIdentityOwnedPath, error) {
	return communicationIdentityOwnedImagePathsAt(root, os.Lstat)
}

func communicationIdentityOwnedImagePathsAt(root string, lstat func(string) (os.FileInfo, error)) ([]CommunicationIdentityOwnedPath, error) {
	rootInfo, err := lstat(root)
	if err != nil {
		return nil, err
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("root stat does not expose device information")
	}
	rootDevice := rootStat.Dev
	var owned []CommunicationIdentityOwnedPath
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		info, err := lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("stat does not expose ownership for %s", path)
		}
		if stat.Dev != rootDevice {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if IsReservedCommunicationIdentity(stat.Uid) || IsReservedCommunicationIdentity(stat.Gid) {
			owned = append(owned, CommunicationIdentityOwnedPath{Path: path, UID: stat.Uid, GID: stat.Gid})
		}
		return nil
	})
	return owned, err
}
