package licensing

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	leaseFileName      = "lease.json"
	installationIDName = "installation.id"
)

func loadLease(stateDir string) (*SignedLease, error) {
	return loadLeaseWithKey(stateDir, nil)
}

type encryptedState struct {
	Version    int    `json:"version"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

func loadLeaseWithKey(stateDir string, key []byte) (*SignedLease, error) {
	path := filepath.Join(stateDir, leaseFileName)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	plain, err := decryptState(b, key)
	if err != nil {
		return nil, fmt.Errorf("decrypt lease: %w", err)
	}
	var lease SignedLease
	if err := decodeJSON(plain, &lease); err != nil {
		return nil, fmt.Errorf("decode lease: %w", err)
	}
	return &lease, nil
}

func persistLease(stateDir string, lease SignedLease) error {
	return persistLeaseWithKey(stateDir, lease, nil)
}

func persistLeaseWithKey(stateDir string, lease SignedLease, key []byte) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	b, err := marshalJSON(lease)
	if err != nil {
		return err
	}
	b, err = encryptState(b, key)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, ".lease-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(b)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(stateDir, leaseFileName))
}

const standardStateFileName = "standard.json"

func removeStandardState(stateDir string) error {
	err := os.Remove(filepath.Join(stateDir, standardStateFileName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func loadStandardState(stateDir string) (*StandardResult, error) {
	return loadStandardStateWithKey(stateDir, nil)
}

func loadStandardStateWithKey(stateDir string, key []byte) (*StandardResult, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, standardStateFileName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := decryptState(b, key)
	if err != nil {
		return nil, err
	}
	var result StandardResult
	if err := decodeJSON(plain, &result); err != nil {
		return nil, err
	}
	if err := validateStandardResult(result); err != nil {
		return nil, err
	}
	return &result, nil
}

func persistStandardState(stateDir string, result StandardResult) error {
	return persistStandardStateWithKey(stateDir, result, nil)
}

func persistStandardStateWithKey(stateDir string, result StandardResult, key []byte) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	b, err := marshalJSON(result)
	if err != nil {
		return err
	}
	b, err = encryptState(b, key)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, ".standard-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(0o600)
	if _, err = tmp.Write(b); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(stateDir, standardStateFileName))
}

func encryptState(plain, key []byte) ([]byte, error) {
	if len(key) == 0 {
		return plain, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, plain, nil)
	return marshalJSON(encryptedState{Version: 1, Nonce: encodeBytes(nonce), Ciphertext: encodeBytes(ciphertext)})
}

func decryptState(data, key []byte) ([]byte, error) {
	if len(key) == 0 {
		return data, nil
	}
	var envelope encryptedState
	if err := decodeJSON(data, &envelope); err != nil || envelope.Version != 1 || strings.TrimSpace(envelope.Nonce) == "" {
		// Existing installations may still have signed plaintext state. Read it
		// once and rewrite it encrypted on the next successful refresh.
		return data, nil
	}
	nonce, err := decodeBytes(envelope.Nonce)
	if err != nil {
		return nil, err
	}
	ciphertext, err := decodeBytes(envelope.Ciphertext)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid state nonce")
	}
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func encodeBytes(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }

func decodeBytes(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
}
