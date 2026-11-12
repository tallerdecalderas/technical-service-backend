package domain

import "time"

type ServicePart struct {
	ID              string    `json:"id"`
	ServiceID       string    `json:"serviceId"`
	InventoryItemID *string   `json:"inventoryItemId,omitempty"`
	Name            string    `json:"name"`
	Nota            *string   `json:"nota,omitempty"`
	ProviderName    *string   `json:"providerName,omitempty"`
	CostUsd         *string   `json:"costUsd,omitempty"`
	ExchangeRate    *string   `json:"exchangeRate,omitempty"`
	Quantity        string    `json:"quantity"`
	UnitCost        string    `json:"unitCost"`
	TotalCost       string    `json:"totalCost"`
	UnitSalePrice   string    `json:"unitSalePrice"`
	TotalSalePrice  string    `json:"totalSalePrice"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type ServicePartDraft struct {
	ServiceID       string
	InventoryItemID *string
	Name            string
	Nota            *string
	ProviderName    *string
	CostUsd         *string
	ExchangeRate    *string
	Quantity        string
	UnitCost        string
	UnitSalePrice   string
}

type ServicePartPatch struct {
	Name          Field[string]
	Nota          Field[string]
	Quantity      Field[string]
	UnitCost      Field[string]
	UnitSalePrice Field[string]
}
