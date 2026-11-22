package runvalidation

import (
	"encoding/base64"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestValidateBinaries32MiBFileBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
		want string
	}{
		{name: "32 MiB", size: 32 << 20},
		{name: "32 MiB plus one", size: (32 << 20) + 1, want: "binary too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := model.Binary{Name: "Main", DataB64: zeroBinaryBase64(tc.size), Mode: "exec"}
			err := ValidateBinaries("binaries", []model.Binary{binary})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validate %d-byte binary: %v", tc.size, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate %d-byte binary: %v, want %q", tc.size, err, tc.want)
			}
		})
	}
}

func TestValidateBinariesKeeps48MiBTotalBoundary(t *testing.T) {
	// Three files share one encoded buffer, so testing the aggregate boundary
	// does not allocate a separate 48 MiB source payload.
	data := zeroBinaryBase64(16 << 20)
	binaries := []model.Binary{
		{Name: "a", DataB64: data},
		{Name: "b", DataB64: data},
		{Name: "c", DataB64: data},
	}
	if err := ValidateBinaries("binaries", binaries); err != nil {
		t.Fatalf("validate 48 MiB total: %v", err)
	}
	binaries = append(binaries, model.Binary{Name: "d", DataB64: "AA=="})
	if err := ValidateBinaries("binaries", binaries); err == nil || !strings.Contains(err.Error(), "binaries total size exceeded") {
		t.Fatalf("validate 48 MiB plus one: %v, want aggregate size rejection", err)
	}
}

func zeroBinaryBase64(size int) string {
	// Zero bytes encode as A's; construct the encoded fixture directly instead
	// of also retaining the decoded source allocation.
	padding := (3 - size%3) % 3
	return strings.Repeat("A", base64.StdEncoding.EncodedLen(size)-padding) + strings.Repeat("=", padding)
}
