package licensing

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstanceIDPersists(t *testing.T) {
	dir := t.TempDir()
	a, err := instanceID(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := instanceID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || len(a) != 64 {
		t.Fatalf("instance id not stable: %q %q", a, b)
	}
	if info, err := os.Stat(filepath.Join(dir, installationIDName)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("installation id permissions: %v %v", info, err)
	}
}
