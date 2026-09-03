package licensing

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func instanceID(stateDir string) (string, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(stateDir, installationIDName)
	seed, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		rawSeed := make([]byte, 32)
		if _, err = rand.Read(rawSeed); err != nil {
			return "", err
		}
		seed = []byte(hex.EncodeToString(rawSeed))
		tmp, err := os.CreateTemp(stateDir, ".installation-*.tmp")
		if err != nil {
			return "", err
		}
		name := tmp.Name()
		defer os.Remove(name)
		_ = tmp.Chmod(0o600)
		if _, err = tmp.Write(seed); err == nil {
			err = tmp.Close()
		} else {
			_ = tmp.Close()
		}
		if err != nil {
			return "", err
		}
		if err = os.Rename(name, path); err != nil {
			return "", err
		}
	}
	seed = []byte(strings.TrimSpace(string(seed)))
	machine := ""
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, e := os.ReadFile(p); e == nil && strings.TrimSpace(string(b)) != "" {
			machine = strings.TrimSpace(string(b))
			break
		}
	}
	host, _ := os.Hostname()
	h := sha256.New()
	_, _ = h.Write(seed)
	_, _ = h.Write([]byte("\x00"))
	_, _ = h.Write([]byte(machine))
	_, _ = h.Write([]byte("\x00"))
	_, _ = h.Write([]byte(host))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func shortID(v string) string {
	if len(v) <= 12 {
		return v
	}
	return fmt.Sprintf("%s…%s", v[:6], v[len(v)-4:])
}
