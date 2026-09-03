package licensing

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestValidateClaim(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	claim := Claim{SchemaVersion: 1, ActivationURL: "https://example.com/activate", ActivationToken: "token", LicenseID: "lic", ExpiresAt: expires, KeyID: "k"}
	payload, _ := marshalJSON(signedClaim{SchemaVersion: claim.SchemaVersion, ActivationURL: claim.ActivationURL, ActivationToken: claim.ActivationToken, LicenseID: claim.LicenseID, ExpiresAt: claim.ExpiresAt, KeyID: claim.KeyID})
	claim.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	if err := validateClaim(claim, pub, time.Now()); err != nil {
		t.Fatal(err)
	}
	claim.ActivationURL, _ = url.QueryUnescape("https://evil.example/activate")
	if err := validateClaim(claim, pub, time.Now()); err == nil {
		t.Fatal("expected signature failure")
	}
}

func TestValidateClaimAcceptsRawURLSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	claim := Claim{
		SchemaVersion:   1,
		ActivationURL:   "https://example.com/activate",
		ActivationToken: "token",
		LicenseID:       "lic",
		ExpiresAt:       time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		KeyID:           "k",
	}
	payload, _ := marshalJSON(signedClaim{SchemaVersion: claim.SchemaVersion, ActivationURL: claim.ActivationURL, ActivationToken: claim.ActivationToken, LicenseID: claim.LicenseID, ExpiresAt: claim.ExpiresAt, KeyID: claim.KeyID})
	claim.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, payload))
	if err := validateClaim(claim, pub, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestStandardClientAcceptsDataEnvelopes(t *testing.T) {
	var activationSeen, verifySeen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/activate"):
			activationSeen = true
			_, _ = w.Write([]byte(`{"data":{"activated":true,"license_id":"lic","config":{"license_key":"key","license_verification_url":"` + "http://" + r.Host + `/verify","machine_code":"machine"}}}`))
		case strings.HasSuffix(r.URL.Path, "/verify"):
			verifySeen = true
			_, _ = w.Write([]byte(`{"data":{"valid":true}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := &Client{httpClient: srv.Client()}
	claim := Claim{LicenseID: "lic", ActivationToken: "token", ActivationURL: srv.URL + "/activate"}
	result, err := client.ActivateClaim(context.Background(), claim, "machine")
	if err != nil {
		t.Fatal(err)
	}
	if !activationSeen || result.LicenseKey != "key" || result.MachineCode != "machine" {
		t.Fatalf("unexpected activation result: %+v", result)
	}
	if err := client.VerifyStandard(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if !verifySeen {
		t.Fatal("verification request was not observed")
	}
}

func TestStandardClientRejectsRemoteHTTPVerification(t *testing.T) {
	result := StandardResult{
		LicenseID:       "lic",
		LicenseKey:      "key",
		MachineCode:     "machine",
		VerificationURL: "http://license.example/verify",
	}
	if err := (&Client{}).VerifyStandard(context.Background(), result); err == nil {
		t.Fatal("expected remote HTTP endpoint to be rejected")
	}
}
