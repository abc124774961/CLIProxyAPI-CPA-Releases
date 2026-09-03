package licensing

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

func verifySignedLease(s SignedLease, publicKey []byte, expectedProduct, expectedInstance string, nowUnix int64) (Lease, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return Lease{}, fmt.Errorf("invalid Ed25519 public key length")
	}
	lease := s.NormalizedLease()
	if lease.LicenseID == "" || lease.ProductCode == "" || lease.InstanceID == "" || lease.Nonce == "" {
		return Lease{}, fmt.Errorf("lease is missing required fields")
	}
	if expectedProduct != "" && lease.ProductCode != expectedProduct {
		return Lease{}, fmt.Errorf("license product mismatch")
	}
	if expectedInstance != "" && lease.InstanceID != expectedInstance {
		return Lease{}, fmt.Errorf("license instance mismatch")
	}
	if lease.IssuedAt <= 0 || lease.ExpiresAt <= 0 || lease.LeaseExpiresAt <= 0 || lease.LeaseExpiresAt < lease.IssuedAt {
		return Lease{}, fmt.Errorf("lease timestamps are invalid")
	}
	if strings.TrimSpace(s.Signature) == "" {
		return Lease{}, fmt.Errorf("lease signature is missing")
	}
	sig, err := decodeSignature(s.Signature)
	if err != nil {
		return Lease{}, err
	}
	payload, err := signedPayload(s)
	if err != nil {
		return Lease{}, err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), payload, sig) {
		return Lease{}, fmt.Errorf("lease signature verification failed")
	}
	if nowUnix < lease.IssuedAt-300 {
		return Lease{}, fmt.Errorf("lease issued-at is in the future")
	}
	return lease, nil
}

func signedPayload(s SignedLease) ([]byte, error) {
	lease := s.NormalizedLease()
	canonical, err := marshalJSON(lease)
	if err != nil {
		return nil, err
	}
	if raw := strings.TrimSpace(s.Payload); raw != "" {
		var payload []byte
		if b, err := base64.RawURLEncoding.DecodeString(raw); err == nil {
			payload = b
		}
		if len(payload) == 0 {
			if b, err := base64.StdEncoding.DecodeString(raw); err == nil {
				payload = b
			}
		}
		if len(payload) == 0 {
			payload = []byte(raw)
		}
		// A provider payload is accepted only when it describes exactly the
		// lease that is being verified. Otherwise an attacker could keep a
		// valid signature over one lease while replacing the outer lease fields
		// used for product, instance, or expiry checks.
		var payloadLease Lease
		if err := decodeJSON(payload, &payloadLease); err != nil {
			return nil, errors.New("license payload is not a lease object")
		}
		payloadCanonical, err := marshalJSON(payloadLease)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(payloadCanonical, canonical) {
			return nil, errors.New("license payload does not match lease")
		}
		return payload, nil
	}
	return canonical, nil
}

func decodeSignature(v string) ([]byte, error) {
	v = strings.TrimSpace(v)
	if b, err := base64.RawURLEncoding.DecodeString(v); err == nil && len(b) == ed25519.SignatureSize {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) == ed25519.SignatureSize {
		return b, nil
	}
	return nil, fmt.Errorf("invalid lease signature encoding")
}
