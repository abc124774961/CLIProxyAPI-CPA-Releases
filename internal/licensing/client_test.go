package licensing

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testSignedLeaseWire(t *testing.T) []byte {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	lease := Lease{
		LicenseID: "license-test", ProductCode: "CPA", InstanceID: "instance-test",
		IssuedAt: now - 1, ExpiresAt: now + 3600, LeaseExpiresAt: now + 3600, Nonce: "nonce-test",
	}
	payload, err := marshalJSON(lease)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := marshalJSON(SignedLease{
		Lease:     lease,
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestClientAcceptsDataEnvelope(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now().Unix()
	lease := Lease{LicenseID: "l", ProductCode: "CPA", InstanceID: "i", IssuedAt: now - 1, ExpiresAt: now + 100, LeaseExpiresAt: now + 50, Nonce: "n"}
	payload, _ := marshalJSON(lease)
	signed := SignedLease{Lease: lease, Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, payload))}
	wire, _ := marshalJSON(map[string]any{"data": signed})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(wire)
	}))
	defer srv.Close()
	cfg := Config{APIBaseURL: srv.URL, ProductCode: "CPA", ActivatePath: "/activate", RefreshPath: "/refresh", VerifyPath: "/verify"}
	got, err := newClient(cfg).Activate(context.Background(), "code", "i")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifySignedLease(got, pub, "CPA", "i", now); err != nil {
		t.Fatal(err)
	}
}

func TestClientOmitsAuthorizationWhenSecretIsUnset(t *testing.T) {
	wire := testSignedLeaseWire(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization header = %q, want empty", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(wire)
	}))
	defer srv.Close()

	cfg := Config{
		APIBaseURL: srv.URL, ProductCode: "CPA", ActivatePath: "/activate",
		RefreshPath: "/refresh", VerifyPath: "/verify", ClientSecret: "",
	}
	if _, err := newClient(cfg).Activate(context.Background(), "code", "instance-test"); err != nil {
		t.Fatal(err)
	}
}

func TestClientTreatsWhitespaceSecretAsUnset(t *testing.T) {
	wire := testSignedLeaseWire(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization header = %q, want empty", got)
		}
		_, _ = w.Write(wire)
	}))
	defer srv.Close()

	cfg := Config{APIBaseURL: srv.URL, ProductCode: "CPA", ActivatePath: "/activate", ClientSecret: "  \t"}
	if _, err := newClient(cfg).Activate(context.Background(), "code", "instance-test"); err != nil {
		t.Fatal(err)
	}
}

func TestClientRetriesProviderRejectedWithoutSecret(t *testing.T) {
	wire := testSignedLeaseWire(t)
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		switch attempts {
		case 1:
			if got := r.Header.Get("Authorization"); got != "Bearer stale-secret" {
				t.Errorf("first Authorization header = %q, want stale secret", got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"code":"provider_rejected"}`)
		case 2:
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("retry Authorization header = %q, want empty", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(wire)
		default:
			t.Errorf("unexpected request attempt %d", attempts)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	cfg := Config{
		APIBaseURL: srv.URL, ProductCode: "CPA", ActivatePath: "/activate",
		RefreshPath: "/refresh", VerifyPath: "/verify", ClientSecret: "stale-secret",
	}
	if _, err := newClient(cfg).Activate(context.Background(), "code", "instance-test"); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("request attempts = %d, want 2", attempts)
	}
}

func TestClientPluginDownloadRetriesProviderRejectedWithoutSecret(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			if got := r.Header.Get("Authorization"); got != "Bearer stale-secret" {
				t.Errorf("first Authorization header = %q, want stale secret", got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"code":"provider_rejected"}`)
			return
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("retry Authorization header = %q, want empty", got)
		}
		_, _ = w.Write([]byte("plugin-bytes"))
	}))
	defer srv.Close()

	cfg := Config{APIBaseURL: srv.URL, ClientSecret: "stale-secret"}
	data, err := newClient(cfg).DownloadEncryptedPlugin(context.Background(), SignedLease{}, "plugin", "1.0.0", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "plugin-bytes" {
		t.Fatalf("plugin response = %q, want plugin-bytes", data)
	}
	if attempts != 2 {
		t.Fatalf("request attempts = %d, want 2", attempts)
	}
}
