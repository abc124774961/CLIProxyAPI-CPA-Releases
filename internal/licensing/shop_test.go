package licensing

import (
	"strings"
	"testing"
	"time"
)

func TestShopStateBindsOriginAndIsConsumedOnExchange(t *testing.T) {
	m := &Manager{
		cfg:        Config{ProductCode: "CPA", ShopAuthURL: "https://shop.example.test/authorize", ShopExchangePath: "/exchange"},
		instance:   "instance-1",
		shopStates: make(map[string]shopState),
	}
	authorization, err := m.StartShopAuthorization("http://manager.example.test/license/shop/callback", "http://manager.example.test", "operator-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(authorization.URL, "state=") || !strings.Contains(authorization.URL, "redirect_uri=") || !strings.Contains(authorization.URL, "instance_id=instance-1") {
		t.Fatalf("authorization URL is incomplete: %s", authorization.URL)
	}
	if err := m.ValidateShopState(authorization.State); err != nil {
		t.Fatal(err)
	}
	m.shopMu.Lock()
	state := m.shopStates[authorization.State]
	m.shopMu.Unlock()
	if state.OperatorID != "operator-1" || state.InstanceID != "instance-1" || state.Origin != "http://manager.example.test" {
		t.Fatalf("unexpected shop state: %+v", state)
	}

	m.shopMu.Lock()
	state.ExpiresAt = time.Now().Add(-time.Second)
	m.shopStates[authorization.State] = state
	m.shopMu.Unlock()
	if err := m.ValidateShopState(authorization.State); err == nil {
		t.Fatal("expected expired state")
	}
}

func TestShopCallbackHTMLDoesNotExposeLeaseFields(t *testing.T) {
	m := &Manager{
		cfg:        Config{},
		instance:   "instance-1",
		shopStates: map[string]shopState{"state": {OperatorID: "operator", InstanceID: "instance-1", Origin: "https://manager.example.test", ExpiresAt: time.Now().Add(time.Minute)}},
	}
	html, err := m.ShopCallbackHTML("state", "one-time-code", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "lease_key") || strings.Contains(html, "client_secret") {
		t.Fatalf("callback HTML contains sensitive field: %s", html)
	}
	if !strings.Contains(html, "one-time-code") || !strings.Contains(html, "postMessage") {
		t.Fatalf("callback HTML missing one-time callback payload: %s", html)
	}
}
