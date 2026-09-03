package licensing

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const claimSchemaVersion = 1

type Claim struct {
	SchemaVersion   int    `json:"schema_version"`
	ActivationURL   string `json:"activation_url"`
	ActivationToken string `json:"activation_token"`
	LicenseID       string `json:"license_id"`
	ExpiresAt       string `json:"expires_at"`
	KeyID           string `json:"key_id"`
	Signature       string `json:"signature"`
}

type signedClaim struct {
	SchemaVersion   int    `json:"schema_version"`
	ActivationURL   string `json:"activation_url"`
	ActivationToken string `json:"activation_token"`
	LicenseID       string `json:"license_id"`
	ExpiresAt       string `json:"expires_at"`
	KeyID           string `json:"key_id"`
}

type StandardResult struct {
	LicenseID             string `json:"license_id"`
	LicenseKey            string `json:"license_key"`
	VerificationURL       string `json:"license_verification_url"`
	MachineCode           string `json:"machine_code"`
	OnlineLicenseRequired bool   `json:"online_license_required"`
	ExpiresAt             string `json:"expires_at,omitempty"`
	ActivatedAt           int64  `json:"activated_at"`
	LastVerifiedAt        int64  `json:"last_verified_at"`
}

type standardActivationRequest struct {
	ActivationToken string `json:"activation_token"`
	MachineCode     string `json:"machine_code"`
	MachineLabel    string `json:"machine_label"`
	Platform        string `json:"platform"`
}
type standardActivationResponse struct {
	Activated bool   `json:"activated"`
	LicenseID string `json:"license_id"`
	Error     string `json:"error"`
	Config    struct {
		LicenseKey            string `json:"license_key"`
		VerificationURL       string `json:"license_verification_url"`
		MachineCode           string `json:"machine_code"`
		OnlineLicenseRequired bool   `json:"online_license_required"`
		ExpiresAt             string `json:"expires_at,omitempty"`
	} `json:"config"`
	// Some storefront deployments flatten config fields while retaining the
	// same activation response envelope. Accept both shapes.
	LicenseKey            string `json:"license_key,omitempty"`
	VerificationURL       string `json:"license_verification_url,omitempty"`
	MachineCode           string `json:"machine_code,omitempty"`
	OnlineLicenseRequired bool   `json:"online_license_required,omitempty"`
	ExpiresAt             string `json:"expires_at,omitempty"`
}

type standardActivationEnvelope struct {
	Data standardActivationResponse `json:"data"`
}
type standardVerifyRequest struct {
	LicenseKey  string `json:"license_key"`
	MachineCode string `json:"machine_code"`
}
type standardVerifyResponse struct {
	Valid bool   `json:"valid"`
	Error string `json:"error"`
}

type standardVerifyEnvelope struct {
	Data standardVerifyResponse `json:"data"`
}

func resolveClaimPath(configured, stateDir string) string {
	candidates := []string{}
	if strings.TrimSpace(configured) != "" {
		candidates = append(candidates, configured)
	}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "activation", "claim.json"))
	}
	candidates = append(candidates, filepath.Join("activation", "claim.json"), filepath.Join(stateDir, "claim.json"))
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func loadClaim(path string, publicKey []byte, now time.Time) (*Claim, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var claim Claim
	if err := decodeJSON(b, &claim); err != nil {
		return nil, fmt.Errorf("decode activation claim: %w", err)
	}
	if err := validateClaim(claim, publicKey, now); err != nil {
		return nil, err
	}
	return &claim, nil
}

func validateClaim(claim Claim, publicKey []byte, now time.Time) error {
	if claim.SchemaVersion != claimSchemaVersion || strings.TrimSpace(claim.ActivationURL) == "" || strings.TrimSpace(claim.ActivationToken) == "" || strings.TrimSpace(claim.LicenseID) == "" || strings.TrimSpace(claim.KeyID) == "" {
		return errors.New("activation claim fields are incomplete")
	}
	if err := validateEndpointURL(claim.ActivationURL); err != nil {
		return fmt.Errorf("activation endpoint is invalid: %w", err)
	}
	expires, err := time.Parse(time.RFC3339, strings.TrimSpace(claim.ExpiresAt))
	if err != nil || !now.Before(expires) {
		return errors.New("activation claim has expired")
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("publisher public key is invalid")
	}
	payload, err := marshalJSON(signedClaim{SchemaVersion: claim.SchemaVersion, ActivationURL: claim.ActivationURL, ActivationToken: claim.ActivationToken, LicenseID: claim.LicenseID, ExpiresAt: claim.ExpiresAt, KeyID: claim.KeyID})
	if err != nil {
		return err
	}
	sig, err := decodeSignatureAny(claim.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(publicKey), payload, sig) {
		return errors.New("activation claim signature is invalid")
	}
	return nil
}

func validateEndpointURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("endpoint URL is malformed")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())) {
		return errors.New("endpoint must use HTTPS")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func decodeSignatureAny(value string) ([]byte, error) {
	v := strings.TrimSpace(value)
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if decoded, err := encoding.DecodeString(v); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid signature encoding")
}

func validateStandardResult(result StandardResult) error {
	if strings.TrimSpace(result.LicenseID) == "" ||
		strings.TrimSpace(result.LicenseKey) == "" ||
		strings.TrimSpace(result.MachineCode) == "" {
		return errors.New("standard activation state is incomplete")
	}
	if err := validateEndpointURL(result.VerificationURL); err != nil {
		return fmt.Errorf("verification endpoint is invalid: %w", err)
	}
	if strings.TrimSpace(result.ExpiresAt) != "" {
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(result.ExpiresAt)); err != nil {
			return fmt.Errorf("standard activation expiry is invalid: %w", err)
		}
	}
	return nil
}

func (c *Client) ActivateClaim(ctx context.Context, claim Claim, machineCode string) (StandardResult, error) {
	body, err := marshalJSON(standardActivationRequest{ActivationToken: claim.ActivationToken, MachineCode: machineCode, MachineLabel: hostname(), Platform: runtime.GOOS + "-" + runtime.GOARCH})
	if err != nil {
		return StandardResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(claim.ActivationURL), strings.NewReader(string(body)))
	if err != nil {
		return StandardResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI/cpa-license")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return StandardResult{}, fmt.Errorf("activate license: %w", err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return StandardResult{}, fmt.Errorf("read activation response: %w", err)
	}
	var out standardActivationResponse
	if err := decodeJSON(body, &out); err != nil {
		return StandardResult{}, fmt.Errorf("decode activation response: %w", err)
	}
	if !out.Activated {
		var envelope standardActivationEnvelope
		if err := decodeJSON(body, &envelope); err == nil && envelope.Data.Activated {
			out = envelope.Data
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !out.Activated {
		msg := strings.TrimSpace(out.Error)
		if msg == "" {
			msg = resp.Status
		}
		return StandardResult{}, fmt.Errorf("license activation failed: %s", msg)
	}
	licenseID := strings.TrimSpace(out.LicenseID)
	if licenseID == "" {
		licenseID = strings.TrimSpace(claim.LicenseID)
	}
	licenseKey := strings.TrimSpace(out.Config.LicenseKey)
	if licenseKey == "" {
		licenseKey = strings.TrimSpace(out.LicenseKey)
	}
	verificationURL := strings.TrimSpace(out.Config.VerificationURL)
	if verificationURL == "" {
		verificationURL = strings.TrimSpace(out.VerificationURL)
	}
	responseMachineCode := strings.TrimSpace(out.Config.MachineCode)
	if responseMachineCode == "" {
		responseMachineCode = strings.TrimSpace(out.MachineCode)
	}
	onlineRequired := out.Config.OnlineLicenseRequired || out.OnlineLicenseRequired
	expiresAt := strings.TrimSpace(out.Config.ExpiresAt)
	if expiresAt == "" {
		expiresAt = strings.TrimSpace(out.ExpiresAt)
	}
	if licenseID != claim.LicenseID || licenseKey == "" || verificationURL == "" || responseMachineCode != machineCode {
		return StandardResult{}, errors.New("activation response does not match this package")
	}
	result := StandardResult{LicenseID: licenseID, LicenseKey: licenseKey, VerificationURL: verificationURL, MachineCode: machineCode, OnlineLicenseRequired: onlineRequired, ExpiresAt: expiresAt, ActivatedAt: time.Now().Unix()}
	if err := validateStandardResult(result); err != nil {
		return StandardResult{}, err
	}
	return result, nil
}

func (c *Client) VerifyStandard(ctx context.Context, result StandardResult) error {
	if err := validateStandardResult(result); err != nil {
		return err
	}
	body, err := marshalJSON(standardVerifyRequest{LicenseKey: result.LicenseKey, MachineCode: result.MachineCode})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(result.VerificationURL, "/"), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI/cpa-license")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("verify license: %w", err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("read license verification: %w", err)
	}
	var out standardVerifyResponse
	if err := decodeJSON(body, &out); err != nil {
		return fmt.Errorf("decode license verification: %w", err)
	}
	if !out.Valid {
		var envelope standardVerifyEnvelope
		if err := decodeJSON(body, &envelope); err == nil && envelope.Data.Valid {
			out = envelope.Data
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !out.Valid {
		if strings.TrimSpace(out.Error) != "" {
			return fmt.Errorf("license verification failed: %s", out.Error)
		}
		return errors.New("license verification failed")
	}
	return nil
}

func hostname() string { h, _ := os.Hostname(); return h }
