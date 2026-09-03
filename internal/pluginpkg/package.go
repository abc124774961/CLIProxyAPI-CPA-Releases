// Package pluginpkg defines the encrypted CPA premium-plugin container.
//
// The package is intentionally independent from the dynamic plugin host and
// the storefront. The storefront only needs Build, while CPA uses Open after
// deriving a key from its in-memory signed lease. Long-lived license material
// is never written into the archive or returned to the browser.
package pluginpkg

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	SchemaVersion       = 1
	ManifestName        = "manifest.json"
	SignatureName       = "signature.json"
	PayloadName         = "payload.bin"
	DefaultMaxPackage   = 128 << 20
	DefaultMaxPayload   = 96 << 20
	SignatureAlgorithm  = "ed25519"
	encryptionAlgorithm = "aes-256-gcm"
)

var (
	idPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	versionPattern = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]*$`)
)

// Manifest is signed as its exact JSON representation. Field order is kept
// stable by the struct declaration and encoding/json.
type Manifest struct {
	SchemaVersion    int      `json:"schema_version"`
	PluginID         string   `json:"plugin_id"`
	Version          string   `json:"version"`
	GOOS             string   `json:"goos"`
	GOARCH           string   `json:"goarch"`
	RequiredFeatures []string `json:"required_features"`
	Payload          string   `json:"payload"`
	PayloadSize      int64    `json:"payload_size"`
	PayloadSHA256    string   `json:"payload_sha256"`
	CipherSHA256     string   `json:"cipher_sha256"`
	Nonce            string   `json:"nonce"`
	Cipher           string   `json:"cipher"`
}

type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id,omitempty"`
	Signature string `json:"signature"`
}

type BuildOptions struct {
	PluginID         string
	Version          string
	GOOS             string
	GOARCH           string
	RequiredFeatures []string
	LicenseID        string
	InstanceID       string
	LeaseNonce       string
	SigningKey       ed25519.PrivateKey
	SigningKeyID     string
	Library          []byte
}

type OpenOptions struct {
	GOOS             string
	GOARCH           string
	RequiredFeatures []string
	LicenseID        string
	InstanceID       string
	LeaseNonce       string
	VerifyKey        ed25519.PublicKey
	MaxPackageSize   int64
	MaxPayloadSize   int64
}

type Opened struct {
	Manifest Manifest
	Payload  []byte
}

// PeekManifest reads the signed package manifest without decrypting the
// payload.  Callers must still pass the returned package through Open before
// trusting any manifest field or using the payload.  This is used when the
// storefront request asks for the newest version and the exact version is
// therefore not known until the package has been received.
func PeekManifest(data []byte) (Manifest, error) {
	if len(data) == 0 {
		return Manifest{}, errors.New("plugin package is empty")
	}
	if int64(len(data)) > DefaultMaxPackage {
		return Manifest{}, errors.New("plugin package is too large")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Manifest{}, fmt.Errorf("open plugin package: %w", err)
	}
	var raw []byte
	for _, file := range reader.File {
		name, errName := cleanEntryName(file.Name)
		if errName != nil {
			return Manifest{}, errName
		}
		if name != ManifestName {
			continue
		}
		if file.FileInfo().IsDir() || file.UncompressedSize64 > 1<<20 {
			return Manifest{}, errors.New("plugin manifest is too large")
		}
		handle, errOpen := file.Open()
		if errOpen != nil {
			return Manifest{}, errOpen
		}
		raw, err = io.ReadAll(io.LimitReader(handle, 1<<20+1))
		_ = handle.Close()
		if err != nil {
			return Manifest{}, err
		}
		if len(raw) > 1<<20 {
			return Manifest{}, errors.New("plugin manifest is too large")
		}
		break
	}
	if len(raw) == 0 {
		return Manifest{}, fmt.Errorf("plugin package missing %s", ManifestName)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode plugin manifest: %w", err)
	}
	if manifest.SchemaVersion != SchemaVersion || !idPattern.MatchString(strings.TrimSpace(manifest.PluginID)) || !versionPattern.MatchString(strings.TrimSpace(manifest.Version)) {
		return Manifest{}, errors.New("plugin manifest fields are invalid")
	}
	return manifest, nil
}

func DeriveKey(licenseID, instanceID, leaseNonce, pluginID, version string) ([]byte, error) {
	parts := []string{strings.TrimSpace(licenseID), strings.TrimSpace(instanceID), strings.TrimSpace(leaseNonce), strings.TrimSpace(pluginID), strings.TrimSpace(version)}
	for _, value := range parts {
		if value == "" {
			return nil, errors.New("plugin key material is incomplete")
		}
	}
	sum := sha256.Sum256([]byte("CLIProxyAPI/plugin-key/v1\x00" + strings.Join(parts, "\x00")))
	key := make([]byte, len(sum))
	copy(key, sum[:])
	return key, nil
}

func Build(options BuildOptions) ([]byte, error) {
	options.PluginID = strings.TrimSpace(options.PluginID)
	options.Version = normalizeVersion(options.Version)
	options.GOOS = normalizeGOOS(options.GOOS)
	options.GOARCH = normalizeGOARCH(options.GOARCH)
	if !idPattern.MatchString(options.PluginID) {
		return nil, fmt.Errorf("invalid plugin id")
	}
	if !versionPattern.MatchString(options.Version) {
		return nil, fmt.Errorf("invalid plugin version")
	}
	if options.GOOS == "" || options.GOARCH == "" {
		return nil, errors.New("plugin platform is required")
	}
	if len(options.Library) == 0 || int64(len(options.Library)) > DefaultMaxPayload {
		return nil, errors.New("plugin payload is empty or too large")
	}
	if len(options.SigningKey) != ed25519.PrivateKeySize {
		return nil, errors.New("plugin signing key is invalid")
	}
	key, err := DeriveKey(options.LicenseID, options.InstanceID, options.LeaseNonce, options.PluginID, options.Version)
	if err != nil {
		return nil, err
	}
	features := normalizeFeatures(options.RequiredFeatures)
	if len(features) == 0 {
		return nil, errors.New("plugin required features are empty")
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
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, options.Library, nil)
	plainSum := sha256.Sum256(options.Library)
	cipherSum := sha256.Sum256(ciphertext)
	manifest := Manifest{
		SchemaVersion: SchemaVersion, PluginID: options.PluginID, Version: options.Version,
		GOOS: options.GOOS, GOARCH: options.GOARCH, RequiredFeatures: features,
		Payload: PayloadName, PayloadSize: int64(len(options.Library)),
		PayloadSHA256: hex.EncodeToString(plainSum[:]), CipherSHA256: hex.EncodeToString(cipherSum[:]),
		Nonce: base64.RawURLEncoding.EncodeToString(nonce), Cipher: encryptionAlgorithm,
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	signature := Signature{Algorithm: SignatureAlgorithm, KeyID: strings.TrimSpace(options.SigningKeyID), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(options.SigningKey, manifestRaw))}
	signatureRaw, err := json.Marshal(signature)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for name, data := range map[string][]byte{ManifestName: manifestRaw, SignatureName: signatureRaw, PayloadName: ciphertext} {
		h, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err = h.Write(data); err != nil {
			return nil, err
		}
	}
	if err = zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func Open(data []byte, options OpenOptions) (Opened, error) {
	if len(data) == 0 {
		return Opened{}, errors.New("plugin package is empty")
	}
	maxPackage := options.MaxPackageSize
	if maxPackage <= 0 {
		maxPackage = DefaultMaxPackage
	}
	if int64(len(data)) > maxPackage {
		return Opened{}, errors.New("plugin package is too large")
	}
	maxPayload := options.MaxPayloadSize
	if maxPayload <= 0 {
		maxPayload = DefaultMaxPayload
	}
	if len(options.VerifyKey) != ed25519.PublicKeySize {
		return Opened{}, errors.New("plugin verification key is invalid")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Opened{}, fmt.Errorf("open plugin package: %w", err)
	}
	entries := map[string][]byte{}
	for _, file := range reader.File {
		name, err := cleanEntryName(file.Name)
		if err != nil {
			return Opened{}, err
		}
		if _, exists := entries[name]; exists {
			return Opened{}, fmt.Errorf("duplicate plugin package entry %s", name)
		}
		if name != ManifestName && name != SignatureName && name != PayloadName {
			return Opened{}, fmt.Errorf("unexpected plugin package entry %s", name)
		}
		if file.FileInfo().IsDir() || file.UncompressedSize64 > uint64(maxPayload) {
			return Opened{}, errors.New("plugin package entry is too large")
		}
		rc, err := file.Open()
		if err != nil {
			return Opened{}, err
		}
		content, readErr := io.ReadAll(io.LimitReader(rc, maxPayload+1))
		_ = rc.Close()
		if readErr != nil {
			return Opened{}, readErr
		}
		if int64(len(content)) > maxPayload {
			return Opened{}, errors.New("plugin package entry exceeds limit")
		}
		entries[name] = content
	}
	for _, name := range []string{ManifestName, SignatureName, PayloadName} {
		if _, ok := entries[name]; !ok {
			return Opened{}, fmt.Errorf("plugin package missing %s", name)
		}
	}
	var manifest Manifest
	if err := json.Unmarshal(entries[ManifestName], &manifest); err != nil {
		return Opened{}, fmt.Errorf("decode plugin manifest: %w", err)
	}
	if err := validateManifest(manifest, options); err != nil {
		return Opened{}, err
	}
	var signature Signature
	if err := json.Unmarshal(entries[SignatureName], &signature); err != nil {
		return Opened{}, fmt.Errorf("decode plugin signature: %w", err)
	}
	if signature.Algorithm != SignatureAlgorithm {
		return Opened{}, errors.New("unsupported plugin signature algorithm")
	}
	sig, err := decodeBase64(signature.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(options.VerifyKey, entries[ManifestName], sig) {
		return Opened{}, errors.New("plugin signature verification failed")
	}
	key, err := DeriveKey(options.LicenseID, options.InstanceID, options.LeaseNonce, manifest.PluginID, manifest.Version)
	if err != nil {
		return Opened{}, err
	}
	nonce, err := decodeBase64(manifest.Nonce)
	if err != nil {
		return Opened{}, errors.New("plugin nonce is invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return Opened{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return Opened{}, errors.New("plugin nonce is invalid")
	}
	cipherSum := sha256.Sum256(entries[PayloadName])
	if !strings.EqualFold(hex.EncodeToString(cipherSum[:]), manifest.CipherSHA256) {
		return Opened{}, errors.New("plugin ciphertext checksum mismatch")
	}
	payload, err := gcm.Open(nil, nonce, entries[PayloadName], nil)
	if err != nil {
		return Opened{}, errors.New("plugin payload decryption failed")
	}
	if int64(len(payload)) != manifest.PayloadSize {
		return Opened{}, errors.New("plugin payload size mismatch")
	}
	plainSum := sha256.Sum256(payload)
	if !strings.EqualFold(hex.EncodeToString(plainSum[:]), manifest.PayloadSHA256) {
		return Opened{}, errors.New("plugin payload checksum mismatch")
	}
	return Opened{Manifest: manifest, Payload: payload}, nil
}

func validateManifest(manifest Manifest, options OpenOptions) error {
	if manifest.SchemaVersion != SchemaVersion || !idPattern.MatchString(strings.TrimSpace(manifest.PluginID)) || !versionPattern.MatchString(strings.TrimSpace(manifest.Version)) {
		return errors.New("plugin manifest fields are invalid")
	}
	if manifest.Payload != PayloadName || manifest.Cipher != encryptionAlgorithm || manifest.PayloadSize <= 0 || manifest.PayloadSize > options.MaxPayloadSize && options.MaxPayloadSize > 0 {
		return errors.New("plugin manifest payload is invalid")
	}
	if normalizeGOOS(manifest.GOOS) != normalizeGOOS(options.GOOS) || normalizeGOARCH(manifest.GOARCH) != normalizeGOARCH(options.GOARCH) {
		return errors.New("plugin platform mismatch")
	}
	granted := make(map[string]struct{}, len(options.RequiredFeatures))
	for _, feature := range options.RequiredFeatures {
		if feature = strings.TrimSpace(feature); feature != "" {
			granted[strings.ToLower(feature)] = struct{}{}
		}
	}
	for _, feature := range normalizeFeatures(manifest.RequiredFeatures) {
		if _, ok := granted[strings.ToLower(feature)]; !ok {
			return fmt.Errorf("plugin feature %s is not licensed", feature)
		}
	}
	return nil
}

func normalizeFeatures(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func cleanEntryName(name string) (string, error) {
	if strings.TrimSpace(name) == "" || strings.Contains(name, `\`) || path.IsAbs(name) {
		return "", errors.New("plugin package entry path is invalid")
	}
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != name {
		return "", errors.New("plugin package entry path escapes root")
	}
	return cleaned, nil
}

func decodeBase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(value); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("invalid base64")
}

func normalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 1 && (value[0] == 'v' || value[0] == 'V') {
		value = value[1:]
	}
	return value
}

func normalizeGOOS(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "mac", "macos", "osx":
		return "darwin"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeGOARCH(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}
