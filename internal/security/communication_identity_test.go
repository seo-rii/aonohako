package security

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCommunicationParticipantUIDForIndex(t *testing.T) {
	if CommunicationParticipantSlots != 64 {
		t.Fatalf("CommunicationParticipantSlots = %d, want 64", CommunicationParticipantSlots)
	}
	tests := []struct {
		index int
		want  uint32
		ok    bool
	}{
		{index: 0, want: 65000, ok: true},
		{index: 63, want: 64937, ok: true},
		{index: -1, ok: false},
		{index: 64, ok: false},
	}
	for _, tc := range tests {
		got, ok := CommunicationParticipantUIDForIndex(tc.index)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("CommunicationParticipantUIDForIndex(%d) = (%d, %v), want (%d, %v)", tc.index, got, ok, tc.want, tc.ok)
		}
	}
	for _, id := range []uint32{CommunicationParticipantUIDMin, CommunicationParticipantUIDMax, CommunicationManagerUID} {
		if !IsReservedCommunicationIdentity(id) {
			t.Fatalf("identity %d should be reserved", id)
		}
	}
	for _, id := range []uint32{CommunicationParticipantUIDMin - 1, CommunicationParticipantUIDMax + 1, 65532} {
		if IsReservedCommunicationIdentity(id) {
			t.Fatalf("identity %d should not be reserved", id)
		}
	}
}

func TestValidateCommunicationIdentityReservationAt(t *testing.T) {
	writeFixture := func(t *testing.T, passwd, group, status string) (string, string, string) {
		t.Helper()
		root := t.TempDir()
		passwdPath := filepath.Join(root, "passwd")
		groupPath := filepath.Join(root, "group")
		procRoot := filepath.Join(root, "proc")
		if err := os.WriteFile(passwdPath, []byte(passwd), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(groupPath, []byte(group), 0o600); err != nil {
			t.Fatal(err)
		}
		processDir := filepath.Join(procRoot, "123")
		if err := os.MkdirAll(processDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(processDir, "status"), []byte(status), 0o600); err != nil {
			t.Fatal(err)
		}
		return passwdPath, groupPath, procRoot
	}

	cleanPasswd := "root:x:0:0:root:/root:/bin/sh\n"
	cleanGroup := "root:x:0:\n"
	cleanStatus := "Name:\ttest\nUid:\t1000\t1000\t1000\t1000\nGid:\t1000\t1000\t1000\t1000\nGroups:\t1000 1001\n"
	passwdPath, groupPath, procRoot := writeFixture(t, cleanPasswd, cleanGroup, cleanStatus)
	if err := ValidateCommunicationIdentityReservationAt(passwdPath, groupPath, procRoot); err != nil {
		t.Fatalf("clean reservation validation failed: %v", err)
	}

	tests := []struct {
		name   string
		passwd string
		group  string
		status string
		want   string
	}{
		{name: "passwd uid", passwd: "participant:x:65000:1000::/:/bin/false\n", group: cleanGroup, status: cleanStatus, want: "uid 65000"},
		{name: "passwd primary gid", passwd: "participant:x:1000:64937::/:/bin/false\n", group: cleanGroup, status: cleanStatus, want: "gid 64937"},
		{name: "group gid", passwd: cleanPasswd, group: "participant:x:64999:\n", status: cleanStatus, want: "gid 64999"},
		{name: "manager process uid", passwd: cleanPasswd, group: cleanGroup, status: "Uid:\t65531\t65531\t65531\t65531\nGid:\t1000\t1000\t1000\t1000\n", want: "identity 65531"},
		{name: "participant process gid", passwd: cleanPasswd, group: cleanGroup, status: "Uid:\t1000\t1000\t1000\t1000\nGid:\t64950\t64950\t64950\t64950\n", want: "identity 64950"},
		{name: "supplementary group", passwd: cleanPasswd, group: cleanGroup, status: "Uid:\t1000\t1000\t1000\t1000\nGid:\t1000\t1000\t1000\t1000\nGroups:\t1000 64940\n", want: "identity 64940"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			passwdPath, groupPath, procRoot := writeFixture(t, tc.passwd, tc.group, tc.status)
			err := ValidateCommunicationIdentityReservationAt(passwdPath, groupPath, procRoot)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

type overriddenStatInfo struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (i overriddenStatInfo) Sys() any { return &i.stat }

func TestCommunicationIdentityOwnedImagePaths(t *testing.T) {
	root := t.TempDir()
	ordinary := filepath.Join(root, "ordinary")
	reserved := filepath.Join(root, "reserved")
	if err := os.WriteFile(ordinary, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reserved, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}

	lstat := func(path string) (os.FileInfo, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		stat := *info.Sys().(*syscall.Stat_t)
		if path == reserved {
			stat.Gid = CommunicationParticipantGIDMax
		}
		return overriddenStatInfo{FileInfo: info, stat: stat}, nil
	}
	owned, err := communicationIdentityOwnedImagePathsAt(root, lstat)
	if err != nil {
		t.Fatalf("communicationIdentityOwnedImagePathsAt() error = %v", err)
	}
	if len(owned) != 1 || owned[0].Path != reserved || owned[0].GID != CommunicationParticipantGIDMax {
		t.Fatalf("owned paths = %+v, want reserved fixture", owned)
	}
}
