package model

import (
	"github.com/uptrace/bun"
	"time"
)

type OfflineRequest struct {
	bun.BaseModel `bun:"table:offline_requests"`
	RequestID     string    `bun:",pk" json:"requestId"`
	ProductCode   string    `json:"productId"`
	DeviceType    string    `json:"deviceType"`
	DeviceDigest  string    `json:"deviceDigest"`
	DeviceLabel   string    `json:"deviceLabel"`
	RawBytes      []byte    `json:"-"`
	Fingerprint   string    `json:"-"`
	CreatedBy     string    `json:"-"`
	CreatedAt     time.Time `bun:",nullzero,default:now()" json:"createdAt"`
}

type LicenseIssuance struct {
	bun.BaseModel      `bun:"table:license_issuances"`
	ID                 string     `bun:",pk" json:"id"`
	LicenseID          string     `json:"licenseId"`
	Revision           int64      `json:"revision"`
	OperationID        string     `json:"operationId"`
	OperationHash      string     `json:"-"`
	Kind               string     `json:"kind"`
	RequestID          string     `json:"requestId"`
	BindingID          string     `json:"bindingId"`
	KeyID              string     `json:"keyId"`
	NotBefore          time.Time  `json:"notBefore"`
	ExpiresAt          *time.Time `json:"expiresAt"`
	IssuedAt           time.Time  `json:"issuedAt"`
	RawBytes           []byte     `json:"-"`
	SHA256             string     `bun:"sha256" json:"sha256"`
	FileName           string     `json:"fileName"`
	PreviousIssuanceID string     `bun:",nullzero" json:"previousIssuanceId,omitempty"`
	CreatedBy          string     `json:"createdBy"`
	CreatedAt          time.Time  `bun:",nullzero,default:now()" json:"createdAt"`
}
type OfflineDeviceBinding struct {
	bun.BaseModel     `bun:"table:offline_device_bindings"`
	ID                string     `bun:",pk" json:"id"`
	LicenseID         string     `json:"licenseId"`
	DeviceType        string     `json:"deviceType"`
	DeviceDigest      string     `json:"deviceDigest"`
	DeviceLabel       string     `json:"deviceLabel"`
	RegisteredAt      time.Time  `bun:",nullzero,default:now()" json:"registeredAt"`
	SupersededAt      *time.Time `json:"supersededAt"`
	PreviousBindingID string     `bun:",nullzero" json:"previousBindingId,omitempty"`
}
