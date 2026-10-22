package model

import (
	"github.com/uptrace/bun"
	"time"
)

type CommercialCustomer struct {
	bun.BaseModel   `bun:"table:commercial_customers,alias:commercial_customer"`
	ID              string    `bun:",pk" json:"id"`
	DisplayName     string    `json:"displayName"`
	CustomerType    string    `json:"customerType"`
	ContactName     string    `json:"contactName"`
	ContactMethod   string    `json:"contactMethod"`
	Email           *string   `json:"email"`
	SourceText      string    `json:"sourceText"`
	Notes           string    `json:"notes"`
	IssuanceEnabled bool      `bun:",notnull" json:"issuanceEnabled"`
	LicenseCount    int       `bun:"-" json:"licenseCount"`
	CreatedAt       time.Time `bun:",nullzero,default:now()" json:"createdAt"`
	UpdatedAt       time.Time `bun:",nullzero,default:now()" json:"updatedAt"`
}
