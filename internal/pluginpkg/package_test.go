package pluginpkg

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"testing"
)

func testBuildOptions(t *testing.T) (BuildOptions, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return BuildOptions{
		PluginID: "cpa-advanced-core", Version: "1.0.0", GOOS: "linux", GOARCH: "amd64",
		RequiredFeatures: []string{"scheduler", "advanced_core"}, LicenseID: "license-1",
		InstanceID: "instance-1", LeaseNonce: "nonce-1", SigningKey: priv,
		SigningKeyID: "publisher-1", Library: []byte("dynamic-library-bytes"),
	}, pub
}

func TestBuildOpenRoundTrip(t *testing.T) {
	build, pub := testBuildOptions(t)
	data, err := Build(build)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(data, OpenOptions{GOOS: "linux", GOARCH: "amd64", RequiredFeatures: []string{"advanced_core", "scheduler"}, LicenseID: "license-1", InstanceID: "instance-1", LeaseNonce: "nonce-1", VerifyKey: pub})
	if err != nil {
		t.Fatal(err)
	}
	if string(opened.Payload) != string(build.Library) || opened.Manifest.Version != "1.0.0" {
		t.Fatalf("opened package = %+v payload=%q", opened.Manifest, opened.Payload)
	}
}

func TestOpenRejectsWrongLeaseMaterialAndFeature(t *testing.T) {
	build, pub := testBuildOptions(t)
	data, err := Build(build)
	if err != nil {
		t.Fatal(err)
	}
	base := OpenOptions{GOOS: "linux", GOARCH: "amd64", RequiredFeatures: []string{"advanced_core", "scheduler"}, LicenseID: "license-1", InstanceID: "instance-1", LeaseNonce: "nonce-1", VerifyKey: pub}
	wrong := base
	wrong.LeaseNonce = "nonce-2"
	if _, err := Open(data, wrong); err == nil {
		t.Fatal("wrong nonce unexpectedly opened package")
	}
	missing := base
	missing.RequiredFeatures = []string{"advanced_core"}
	if _, err := Open(data, missing); err == nil {
		t.Fatal("missing feature unexpectedly opened package")
	}
}

func TestOpenRejectsTamperingAndUnsafeEntries(t *testing.T) {
	build, pub := testBuildOptions(t)
	data, err := Build(build)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range r.File {
		if entry.Name == ManifestName {
			rc, _ := entry.Open()
			if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
				t.Fatal(err)
			}
			_ = rc.Close()
		}
	}
	manifest.Version = "1.0.1"
	manifestRaw, _ := json.Marshal(manifest)
	// Replacing only the manifest invalidates the Ed25519 signature.
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, entry := range r.File {
		h, err := zw.Create(entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		var raw []byte
		if entry.Name == ManifestName {
			raw = manifestRaw
		} else {
			rc, _ := entry.Open()
			raw, _ = io.ReadAll(rc)
			_ = rc.Close()
		}
		_, _ = h.Write(raw)
	}
	_ = zw.Close()
	if _, err := Open(out.Bytes(), OpenOptions{GOOS: "linux", GOARCH: "amd64", RequiredFeatures: build.RequiredFeatures, LicenseID: build.LicenseID, InstanceID: build.InstanceID, LeaseNonce: build.LeaseNonce, VerifyKey: pub}); err == nil {
		t.Fatal("tampered manifest unexpectedly opened")
	}
}
