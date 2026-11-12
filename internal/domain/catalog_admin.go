package domain

import "time"

type CatalogProvider struct {
	ID        string    `json:"id"`
	CompanyID string    `json:"companyId"`
	Name      string    `json:"name"`
	IsActive  bool      `json:"isActive"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type CatalogProviderDraft struct {
	CompanyID string
	Name      string
}

type CatalogProviderPatch struct {
	Name     Field[string]
	IsActive Field[bool]
}

type CatalogProviderFilter struct {
	Actor     ServiceActor
	CompanyID string
	Search    string
	Page      int
	PageSize  int
}

type CatalogProviderPage struct {
	Data     []CatalogProvider
	Page     int
	PageSize int
	Total    int
}

type CatalogOffer struct {
	ID              string    `json:"id"`
	CompanyID       string    `json:"companyId"`
	InventoryItemID string    `json:"inventoryItemId"`
	ItemCode        string    `json:"itemCode"`
	ItemName        string    `json:"itemName"`
	ProviderID      string    `json:"providerId"`
	ProviderName    string    `json:"providerName"`
	CostUsd         string    `json:"costUsd"`
	IsActive        bool      `json:"isActive"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type CatalogOfferDraft struct {
	CompanyID       string
	InventoryItemID string
	ProviderID      string
	CostUsd         string
}

type CatalogOfferPatch struct {
	CostUsd  Field[string]
	IsActive Field[bool]
}

type CatalogOfferFilter struct {
	Actor           ServiceActor
	CompanyID       string
	InventoryItemID string
	Page            int
	PageSize        int
}

type CatalogOfferPage struct {
	Data     []CatalogOffer
	Page     int
	PageSize int
	Total    int
}

type ExchangeRate struct {
	ID          string    `json:"id"`
	RateDate    string    `json:"rateDate"`
	Rate        string    `json:"rate"`
	CreatedByID string    `json:"createdById"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type ExchangeRateDraft struct {
	RateDate    string
	Rate        string
	CreatedByID string
}

type CatalogItemPatch struct {
	MarginPercent Field[string]
	TechPrice     Field[string]
	IsActive      Field[bool]
}

type CatalogItemPrice struct {
	ID            string    `json:"id"`
	CompanyID     string    `json:"companyId"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	CostInitList  string    `json:"costInitList"`
	CostPrice     string    `json:"costPrice"`
	SellPrice     string    `json:"sellPrice"`
	TechPrice     string    `json:"techPrice"`
	MarginPercent string    `json:"marginPercent"`
	Gain          string    `json:"gain"`
	IsActive      bool      `json:"isActive"`
	UpdatedAt     time.Time `json:"updatedAt"`
}
