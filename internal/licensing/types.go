package licensing

import "time"

// Lease is the short-lived, signed entitlement issued by the Shop provider.
// The lease is intentionally self-contained so request handling never needs to
// call the remote licensing service.
type Lease struct {
	LicenseID            string   `json:"license_id"`
	ProductCode          string   `json:"product"`
	ProductName          string   `json:"product_name,omitempty"`
	CustomerID           string   `json:"customer_id,omitempty"`
	InstanceID           string   `json:"instance_id"`
	Features             []string `json:"features,omitempty"`
	IssuedAt             int64    `json:"issued_at"`
	ExpiresAt            int64    `json:"expires_at"`
	LeaseExpiresAt       int64    `json:"lease_expires_at"`
	VersionLimit         string   `json:"version_limit,omitempty"`
	Nonce                string   `json:"nonce"`
	Grace                bool     `json:"grace,omitempty"`
	GraceStartedAt       int64    `json:"grace_started_at,omitempty"`
	GraceUntil           int64    `json:"grace_until,omitempty"`
	ExpiryGraceStartedAt int64    `json:"expiry_grace_started_at,omitempty"`
	ExpiryGraceUntil     int64    `json:"expiry_grace_until,omitempty"`
}

func (l Lease) IssuedTime() time.Time       { return time.Unix(l.IssuedAt, 0) }
func (l Lease) ExpiresTime() time.Time      { return time.Unix(l.ExpiresAt, 0) }
func (l Lease) LeaseExpiresTime() time.Time { return time.Unix(l.LeaseExpiresAt, 0) }

// SignedLease supports both the nested response recommended for the Shop API
// and a flattened response for compatibility with existing adapters.
type SignedLease struct {
	Lease     Lease  `json:"lease"`
	Signature string `json:"signature"`
	KeyID     string `json:"kid,omitempty"`
	Payload   string `json:"payload,omitempty"`

	// Flattened fields accepted from providers that do not nest lease.
	LicenseID            string   `json:"license_id,omitempty"`
	ProductCode          string   `json:"product,omitempty"`
	ProductName          string   `json:"product_name,omitempty"`
	CustomerID           string   `json:"customer_id,omitempty"`
	InstanceID           string   `json:"instance_id,omitempty"`
	Features             []string `json:"features,omitempty"`
	IssuedAt             int64    `json:"issued_at,omitempty"`
	ExpiresAt            int64    `json:"expires_at,omitempty"`
	LeaseExpiresAt       int64    `json:"lease_expires_at,omitempty"`
	VersionLimit         string   `json:"version_limit,omitempty"`
	Nonce                string   `json:"nonce,omitempty"`
	Grace                bool     `json:"grace,omitempty"`
	GraceStartedAt       int64    `json:"grace_started_at,omitempty"`
	GraceUntil           int64    `json:"grace_until,omitempty"`
	ExpiryGraceStartedAt int64    `json:"expiry_grace_started_at,omitempty"`
	ExpiryGraceUntil     int64    `json:"expiry_grace_until,omitempty"`
}

func (s SignedLease) NormalizedLease() Lease {
	if s.Lease.LicenseID != "" || s.Lease.ProductCode != "" || s.Lease.InstanceID != "" {
		return s.Lease
	}
	return Lease{
		LicenseID: s.LicenseID, ProductCode: s.ProductCode, ProductName: s.ProductName, CustomerID: s.CustomerID,
		InstanceID: s.InstanceID, Features: s.Features, IssuedAt: s.IssuedAt,
		ExpiresAt: s.ExpiresAt, LeaseExpiresAt: s.LeaseExpiresAt,
		VersionLimit: s.VersionLimit, Nonce: s.Nonce, Grace: s.Grace,
		GraceStartedAt: s.GraceStartedAt, GraceUntil: s.GraceUntil,
		ExpiryGraceStartedAt: s.ExpiryGraceStartedAt, ExpiryGraceUntil: s.ExpiryGraceUntil,
	}
}

// PublicStatus contains only non-sensitive information safe for the admin UI.
type PublicStatus struct {
	Enabled                  bool     `json:"enabled"`
	Configured               bool     `json:"configured"`
	Valid                    bool     `json:"valid"`
	InGrace                  bool     `json:"in_grace"`
	Allowed                  bool     `json:"allowed"`
	Reason                   string   `json:"reason,omitempty"`
	Provider                 string   `json:"provider"`
	ProductCode              string   `json:"product_code"`
	ProductName              string   `json:"product_name,omitempty"`
	LicenseID                string   `json:"license_id,omitempty"`
	ActivationMode           string   `json:"activation_mode,omitempty"`
	Features                 []string `json:"features,omitempty"`
	InstanceID               string   `json:"instance_id"`
	InstanceBound            bool     `json:"instance_bound"`
	ExpiresAt                int64    `json:"expires_at,omitempty"`
	LeaseExpiresAt           int64    `json:"lease_expires_at,omitempty"`
	LastVerifiedAt           int64    `json:"last_verified_at,omitempty"`
	LastRefreshAt            int64    `json:"last_refresh_at,omitempty"`
	LastRefreshError         string   `json:"last_refresh_error,omitempty"`
	IntegrityChecked         bool     `json:"integrity_checked"`
	IntegrityValid           bool     `json:"integrity_valid"`
	GracePeriodSeconds       int64    `json:"grace_period_seconds"`
	GraceStartedAt           int64    `json:"grace_started_at,omitempty"`
	GraceUntil               int64    `json:"grace_until,omitempty"`
	GraceRemainingSecs       int64    `json:"grace_remaining_seconds,omitempty"`
	ExpiryGrace              bool     `json:"expiry_grace,omitempty"`
	ExpiryGraceStartedAt     int64    `json:"expiry_grace_started_at,omitempty"`
	ExpiryGraceUntil         int64    `json:"expiry_grace_until,omitempty"`
	ExpiryGraceRemainingSecs int64    `json:"expiry_grace_remaining_seconds,omitempty"`
}
