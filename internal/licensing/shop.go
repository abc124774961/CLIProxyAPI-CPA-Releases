package licensing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const shopAuthorizationTTL = 5 * time.Minute

type shopState struct {
	OperatorID string
	InstanceID string
	ReturnURL  string
	Origin     string
	ExpiresAt  time.Time
}

// ShopAuthorization is the non-sensitive data needed by the browser to open
// the marketplace. The marketplace only receives a short-lived state value.
type ShopAuthorization struct {
	URL       string    `json:"url"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expires_at"`
}

func deriveStorageKey(cfg Config, instance string) []byte {
	seed := strings.TrimSpace(cfg.StorageKey)
	if seed == "" {
		seed = strings.TrimSpace(cfg.ClientSecret)
	}
	if seed == "" {
		seed = "cpa-license-local:" + cfg.ProductCode + ":" + instance
	}
	digest := sha256.Sum256([]byte(seed + "\x00" + instance))
	return digest[:]
}

func newShopState() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// ValidateShopState performs the non-consuming portion of the callback check.
// The state remains reserved until ExchangeShopCode consumes it, so a browser
// refresh cannot make a valid state reusable.
func (m *Manager) ValidateShopState(state string) error {
	if m == nil {
		return ErrFeature
	}
	state = strings.TrimSpace(state)
	if state == "" {
		return errors.New("shop authorization state is empty")
	}
	m.shopMu.Lock()
	pending, ok := m.shopStates[state]
	if ok && time.Now().After(pending.ExpiresAt) {
		delete(m.shopStates, state)
		ok = false
	}
	m.shopMu.Unlock()
	if !ok {
		return errors.New("shop authorization state expired")
	}
	if pending.InstanceID != m.instance {
		return errors.New("shop authorization state does not match this instance")
	}
	return nil
}

func (m *Manager) StartShopAuthorization(returnURL, origin, operatorID string) (ShopAuthorization, error) {
	if m == nil {
		return ShopAuthorization{}, ErrFeature
	}
	returnURL = strings.TrimSpace(returnURL)
	origin = strings.TrimSpace(origin)
	operatorID = strings.TrimSpace(operatorID)
	if returnURL == "" {
		return ShopAuthorization{}, errors.New("shop callback URL is empty")
	}
	callbackURL, callbackOrigin, err := validateCallbackURL(returnURL, origin)
	if err != nil {
		return ShopAuthorization{}, err
	}
	if operatorID == "" {
		return ShopAuthorization{}, errors.New("management session is unavailable")
	}
	shopURL, err := url.Parse(m.cfg.ShopAuthURL)
	if err != nil || shopURL.Host == "" || (shopURL.Scheme != "https" && shopURL.Scheme != "http") {
		return ShopAuthorization{}, errors.New("shop authorization URL is invalid")
	}
	// Older customer configs used /shop without a trailing slash. Normalize
	// that legacy path before adding the authorization query so a redirecting
	// reverse proxy cannot drop state, instance, or callback parameters.
	if shopURL.Path == "/shop" {
		shopURL.Path = "/shop/"
	}
	state, err := newShopState()
	if err != nil {
		return ShopAuthorization{}, err
	}
	expires := time.Now().Add(shopAuthorizationTTL)
	m.shopMu.Lock()
	if m.shopStates == nil {
		m.shopStates = make(map[string]shopState)
	}
	for key, item := range m.shopStates {
		if time.Now().After(item.ExpiresAt) {
			delete(m.shopStates, key)
		}
	}
	m.shopStates[state] = shopState{OperatorID: operatorID, InstanceID: m.instance, ReturnURL: callbackURL, Origin: callbackOrigin, ExpiresAt: expires}
	m.shopMu.Unlock()

	q := shopURL.Query()
	q.Set("state", state)
	q.Set("redirect_uri", callbackURL)
	q.Set("product", m.cfg.ProductCode)
	// The marketplace binds the one-time browser code to this exact CPA
	// instance before redirecting back. The value is opaque to the marketplace
	// UI and is checked again by the CPA server during exchange.
	q.Set("instance_id", m.instance)
	shopURL.RawQuery = q.Encode()
	return ShopAuthorization{URL: shopURL.String(), State: state, ExpiresAt: expires}, nil
}

func (m *Manager) ExchangeShopCode(ctx context.Context, state, code, operatorID string) error {
	if m == nil {
		return ErrFeature
	}
	state = strings.TrimSpace(state)
	code = strings.TrimSpace(code)
	if state == "" || code == "" {
		return errors.New("shop authorization response is incomplete")
	}
	now := time.Now()
	m.shopMu.Lock()
	pending, ok := m.shopStates[state]
	if ok && now.After(pending.ExpiresAt) {
		delete(m.shopStates, state)
		ok = false
	}
	if !ok {
		m.shopMu.Unlock()
		return errors.New("shop authorization state expired")
	}
	if pending.OperatorID != operatorID || pending.InstanceID != m.instance {
		m.shopMu.Unlock()
		return errors.New("shop authorization state does not match this session")
	}
	// Consume only after all bindings have matched. The delete is performed
	// while holding the same lock as the checks, making concurrent exchanges
	// resolve to exactly one winner.
	delete(m.shopStates, state)
	m.shopMu.Unlock()
	signed, err := m.client.ExchangeShopCode(ctx, m.cfg.ShopExchangePath, code, state, m.instance)
	if err != nil {
		m.setError(err)
		return err
	}
	lease, err := verifySignedLease(signed, m.cfg.PublicKey, m.cfg.ProductCode, expectedInstance(m.cfg, m.instance), now.Unix())
	if err != nil {
		m.setError(err)
		return err
	}
	if err := validateLeaseTimes(lease, now); err != nil {
		m.setError(err)
		return err
	}
	if err := persistLeaseWithKey(m.cfg.StateDir, signed, m.storageKey); err != nil {
		m.setError(err)
		return err
	}
	if err := removeStandardState(m.cfg.StateDir); err != nil {
		m.setError(err)
		return err
	}
	m.mu.Lock()
	m.lease = &signed
	m.standard = nil
	m.verified = now
	m.refreshed = now
	m.lastErr = ""
	m.mu.Unlock()
	return nil
}

func (m *Manager) ShopCallbackHTML(state, code, callbackError string) (string, error) {
	if err := m.ValidateShopState(state); err != nil {
		return "", err
	}
	m.shopMu.Lock()
	pending := m.shopStates[strings.TrimSpace(state)]
	m.shopMu.Unlock()
	// JSON encoding is required here: state, code, and provider error text are
	// untrusted query parameters and must not be able to terminate the script
	// element. The callback only carries the one-time code, never a lease key.
	payload, err := marshalJSON(map[string]string{
		"type":  "cpa-license-callback",
		"state": state,
		"code":  code,
		"error": callbackError,
	})
	if err != nil {
		payload = []byte(`{"type":"cpa-license-callback","state":"","code":"","error":"callback encoding failed"}`)
	}
	originJSON, _ := marshalJSON(pending.Origin)
	return fmt.Sprintf(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="referrer" content="no-referrer"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'"><title>CPA 授权</title></head><body><p>正在返回 CPA...</p><script>
const data=%s;
if(window.opener){window.opener.postMessage(data,%s);window.close();}
</script></body></html>`, string(payload), string(originJSON)), nil
}

func validateCallbackURL(returnURL, expectedOrigin string) (string, string, error) {
	callback, err := url.Parse(returnURL)
	if err != nil || callback.Host == "" || callback.User != nil || callback.RawQuery != "" || callback.Fragment != "" {
		return "", "", errors.New("shop callback URL is invalid")
	}
	if callback.Scheme != "https" && callback.Scheme != "http" {
		return "", "", errors.New("shop callback URL must use HTTP or HTTPS")
	}
	if callback.Path != "/license/shop/callback" {
		return "", "", errors.New("shop callback path is invalid")
	}
	originURL, err := url.Parse(expectedOrigin)
	if err != nil || originURL.Scheme == "" || originURL.Host == "" || originURL.User != nil || originURL.Path != "" || originURL.RawQuery != "" || originURL.Fragment != "" {
		return "", "", errors.New("shop callback origin is invalid")
	}
	if !strings.EqualFold(callback.Scheme, originURL.Scheme) || !strings.EqualFold(callback.Host, originURL.Host) {
		return "", "", errors.New("shop callback origin does not match")
	}
	return callback.String(), originURL.Scheme + "://" + originURL.Host, nil
}
