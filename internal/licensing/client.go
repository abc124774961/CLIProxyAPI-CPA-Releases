package licensing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type providerError struct {
	code string
}

func (e providerError) Error() string { return e.code }

type Client struct {
	baseURL      string
	productCode  string
	activatePath string
	refreshPath  string
	verifyPath   string
	gracePath    string
	clientID     string
	secret       string
	httpClient   *http.Client
}

type providerResponse struct {
	status   int
	body     []byte
	tooLarge bool
}

type activateRequest struct {
	Code       string `json:"code"`
	Product    string `json:"product"`
	InstanceID string `json:"instance_id"`
	ClientID   string `json:"client_id,omitempty"`
}
type refreshRequest struct {
	LicenseID  string `json:"license_id"`
	Product    string `json:"product"`
	InstanceID string `json:"instance_id"`
	Nonce      string `json:"nonce"`
	ClientID   string `json:"client_id,omitempty"`
}

type graceRequest struct {
	Product    string `json:"product"`
	InstanceID string `json:"instance_id"`
	ClientID   string `json:"client_id,omitempty"`
}

type shopExchangeRequest struct {
	Code       string `json:"code"`
	State      string `json:"state"`
	Product    string `json:"product"`
	InstanceID string `json:"instance_id"`
	ClientID   string `json:"client_id,omitempty"`
}

// pluginDownloadRequest is sent only from CPA to the storefront. The signed
// lease is included so the storefront can verify the instance and nonce
// without ever issuing a reusable decryption secret to the browser.
type pluginDownloadRequest struct {
	Authorization SignedLease `json:"authorization"`
	PluginID      string      `json:"plugin_id"`
	Version       string      `json:"version"`
	GOOS          string      `json:"goos"`
	GOARCH        string      `json:"goarch"`
}

func newClient(cfg Config) *Client {
	return &Client{
		baseURL: strings.TrimRight(cfg.APIBaseURL, "/"), productCode: cfg.ProductCode,
		activatePath: cfg.ActivatePath, refreshPath: cfg.RefreshPath, verifyPath: cfg.VerifyPath,
		gracePath: cfg.GracePath,
		clientID:  strings.TrimSpace(cfg.ClientID), secret: strings.TrimSpace(cfg.ClientSecret),
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *Client) Activate(ctx context.Context, code, instanceID string) (SignedLease, error) {
	return c.post(ctx, c.activatePath, activateRequest{Code: strings.TrimSpace(code), Product: c.productCode, InstanceID: instanceID, ClientID: c.clientID})
}
func (c *Client) Refresh(ctx context.Context, lease Lease, instanceID string) (SignedLease, error) {
	return c.post(ctx, c.refreshPath, refreshRequest{LicenseID: lease.LicenseID, Product: c.productCode, InstanceID: instanceID, Nonce: lease.Nonce, ClientID: c.clientID})
}
func (c *Client) Verify(ctx context.Context, lease Lease, instanceID string) (SignedLease, error) {
	return c.post(ctx, c.verifyPath, refreshRequest{LicenseID: lease.LicenseID, Product: c.productCode, InstanceID: instanceID, Nonce: lease.Nonce, ClientID: c.clientID})
}

func (c *Client) AcquireGrace(ctx context.Context, instanceID string) (SignedLease, error) {
	return c.post(ctx, c.gracePath, graceRequest{Product: c.productCode, InstanceID: instanceID, ClientID: c.clientID})
}

func (c *Client) ExchangeShopCode(ctx context.Context, path, code, state, instanceID string) (SignedLease, error) {
	return c.post(ctx, path, shopExchangeRequest{Code: strings.TrimSpace(code), State: strings.TrimSpace(state), Product: c.productCode, InstanceID: instanceID, ClientID: c.clientID})
}

func (c *Client) DownloadEncryptedPlugin(ctx context.Context, signed SignedLease, pluginID, version, goos, goarch string) ([]byte, error) {
	if c == nil {
		return nil, errors.New("license provider is not configured")
	}
	payload, err := marshalJSON(pluginDownloadRequest{
		Authorization: signed,
		PluginID:      strings.TrimSpace(pluginID),
		Version:       strings.TrimSpace(version),
		GOOS:          strings.TrimSpace(goos),
		GOARCH:        strings.TrimSpace(goarch),
	})
	if err != nil {
		return nil, err
	}
	return c.postBytes(ctx, "/licenses/plugin-download", payload)
}

func (c *Client) post(ctx context.Context, path string, payload any) (SignedLease, error) {
	if c.baseURL == "" {
		return SignedLease{}, fmt.Errorf("license provider URL is empty")
	}
	body, err := marshalJSON(payload)
	if err != nil {
		return SignedLease{}, err
	}
	response, err := c.doPost(ctx, path, body, "application/json", 256<<10, c.secret != "")
	if err != nil {
		return SignedLease{}, err
	}
	if response.tooLarge {
		return SignedLease{}, fmt.Errorf("license provider response is too large")
	}
	if response.status < 200 || response.status >= 300 {
		if c.shouldRetryWithoutSecret(response.status, response.body) {
			response, err = c.doPost(ctx, path, body, "application/json", 256<<10, false)
			if err != nil {
				return SignedLease{}, err
			}
			if response.tooLarge {
				return SignedLease{}, fmt.Errorf("license provider response is too large")
			}
		}
		if response.status < 200 || response.status >= 300 {
			return SignedLease{}, providerError{code: providerErrorCode(response.status, response.body)}
		}
	}
	body = response.body
	var result SignedLease
	if err := decodeJSON(body, &result); err == nil && strings.TrimSpace(result.Signature) != "" {
		return result, nil
	}
	// Also accept the common {"data":{...}} envelope used by shop APIs.
	var envelope struct {
		Data   rawMessage `json:"data"`
		Result rawMessage `json:"result"`
	}
	if err := decodeJSON(body, &envelope); err != nil {
		return SignedLease{}, fmt.Errorf("decode license provider response: %w", err)
	}
	for _, raw := range []rawMessage{envelope.Data, envelope.Result} {
		if len(raw) == 0 {
			continue
		}
		if err := decodeJSON(raw, &result); err == nil && strings.TrimSpace(result.Signature) != "" {
			return result, nil
		}
	}
	return SignedLease{}, fmt.Errorf("license provider response does not contain a signed lease")
}

func (c *Client) postBytes(ctx context.Context, path string, body []byte) ([]byte, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("license provider URL is empty")
	}
	const maxPackage = 128 << 20
	response, err := c.doPost(ctx, path, body, "application/octet-stream", maxPackage, c.secret != "")
	if err != nil {
		return nil, err
	}
	if response.tooLarge {
		return nil, fmt.Errorf("plugin package is too large")
	}
	if response.status < 200 || response.status >= 300 {
		if c.shouldRetryWithoutSecret(response.status, response.body) {
			response, err = c.doPost(ctx, path, body, "application/octet-stream", maxPackage, false)
			if err != nil {
				return nil, err
			}
			if response.tooLarge {
				return nil, fmt.Errorf("plugin package is too large")
			}
		}
		if response.status < 200 || response.status >= 300 {
			return nil, providerError{code: providerErrorCode(response.status, response.body)}
		}
	}
	return response.body, nil
}

func (c *Client) shouldRetryWithoutSecret(status int, body []byte) bool {
	return c != nil && strings.TrimSpace(c.secret) != "" && status == http.StatusUnauthorized && providerErrorCode(status, body) == "provider_rejected"
}

func (c *Client) doPost(ctx context.Context, path string, body []byte, accept string, maxBody int64, includeSecret bool) (providerResponse, error) {
	if c == nil || c.httpClient == nil {
		return providerResponse{}, errors.New("license provider client is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(string(body)))
	if err != nil {
		return providerResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	if c.clientID != "" {
		req.Header.Set("X-License-Client-ID", c.clientID)
	}
	if includeSecret && c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return providerResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return providerResponse{}, err
	}
	return providerResponse{status: resp.StatusCode, body: data, tooLarge: int64(len(data)) > maxBody}, nil
}

func providerErrorCode(status int, body []byte) string {
	var payload struct {
		Code  string `json:"code"`
		Error any    `json:"error"`
	}
	_ = decodeJSON(body, &payload)
	code := strings.ToLower(strings.TrimSpace(payload.Code))
	if code == "" {
		switch value := payload.Error.(type) {
		case string:
			code = strings.ToLower(strings.TrimSpace(value))
		case map[string]any:
			code, _ = value["code"].(string)
			code = strings.ToLower(strings.TrimSpace(code))
		}
	}
	switch code {
	case "not_purchased", "purchase_required", "order_not_found":
		return "purchase_required"
	case "order_expired", "license_expired", "expired":
		return "expired"
	case "order_revoked", "license_revoked", "revoked":
		return "revoked"
	case "code_expired", "authorization_expired":
		return "authorization_expired"
	case "code_used", "code_replayed", "state_mismatch", "instance_mismatch":
		return "authorization_invalid"
	default:
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return "provider_rejected"
		}
		if status >= http.StatusInternalServerError {
			return "provider_unavailable"
		}
		return "provider_error"
	}
}

func isProviderError(err error, code string) bool {
	var target providerError
	return errors.As(err, &target) && target.code == code
}
