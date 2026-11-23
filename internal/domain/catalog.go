package domain

import "time"

type CatalogPrice struct {
	ID            string    `json:"id"`
	CompanyID     string    `json:"companyId"`
	CompanyName   string    `json:"companyName"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Unit          *string   `json:"unit,omitempty"`
	TechPrice     string    `json:"techPrice"`
	CostPrice     string    `json:"costPrice,omitempty"`
	SellPrice     string    `json:"sellPrice,omitempty"`
	CostInitList  string    `json:"costInitList,omitempty"`
	MarginPercent string    `json:"marginPercent,omitempty"`
	IsActive      bool      `json:"isActive,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type CatalogFilter struct {
	Actor           ServiceActor
	CompanyID       string
	Search          string
	IncludeInactive bool
	Page            int
	PageSize        int
}

type CatalogPage struct {
	Data      []CatalogPrice `json:"data"`
	Page      int            `json:"page"`
	PageSize  int            `json:"pageSize"`
	Total     int            `json:"total"`
	Version   string         `json:"version"`
	UpdatedAt time.Time      `json:"updatedAt"`
}
