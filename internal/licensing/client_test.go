package licensing

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
