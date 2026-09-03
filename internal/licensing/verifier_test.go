package licensing

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"
)

func TestVerifySignedLease(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now().Unix()
	lease := Lease{LicenseID: "lic_1", ProductCode: "CPA", InstanceID: "inst_1", IssuedAt: now - 10, ExpiresAt: now + 3600, LeaseExpiresAt: now + 600, Nonce: "nonce"}
	payload, err := marshalJSON(lease)
	if err != nil {
		t.Fatal(err)
	}
	signed := SignedLease{Lease: lease, Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, payload))}
	got, err := verifySignedLease(signed, pub, "CPA", "inst_1", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.LicenseID != lease.LicenseID {
		t.Fatalf("unexpected lease: %+v", got)
	}
	signed.Lease.ProductCode = "OTHER"
	if _, err = verifySignedLease(signed, pub, "CPA", "inst_1", now); err == nil {
		t.Fatal("expected product mismatch")
	}
}

func TestVerifySignedLeaseRejectsPayloadLeaseMismatch(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now().Unix()
	signedLease := Lease{LicenseID: "lic_1", ProductCode: "CPA", InstanceID: "inst_1", IssuedAt: now - 10, ExpiresAt: now + 3600, LeaseExpiresAt: now + 600, Nonce: "nonce"}
	payloadLease := signedLease
	payloadLease.ProductCode = "OTHER"
	payload, err := marshalJSON(payloadLease)
	if err != nil {
		t.Fatal(err)
	}
	signed := SignedLease{
		Lease:     signedLease,
		Payload:   base64.RawURLEncoding.EncodeToString(payload),
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, payload)),
	}
	if _, err := verifySignedLease(signed, pub, "CPA", "inst_1", now); err == nil {
		t.Fatal("expected payload/lease mismatch to be rejected")
	}
}
