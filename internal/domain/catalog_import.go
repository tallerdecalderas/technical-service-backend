package domain

import "time"

const (
	CatalogImportPublished = "PUBLISHED"
	CatalogImportFailed    = "FAILED"
)

type CatalogImportError struct {
	Row     int    `json:"row"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

type CatalogImportRow struct {
	Code          string
	Name          string
	Unit          *string
	ProviderName  string
	CostUsd       string
	MarginPercent string
}

type CatalogImport struct {
	ID          string               `json:"id"`
	CompanyID   string               `json:"companyId"`
	StartedByID string               `json:"startedById"`
	Status      string               `json:"status"`
	SourceRef   string               `json:"sourceRef,omitempty"`
	RowCount    int                  `json:"rowCount"`
	ItemCount   int                  `json:"itemCount"`
	ErrorCount  int                  `json:"errorCount"`
	Errors      []CatalogImportError `json:"errors"`
	StartedAt   time.Time            `json:"startedAt"`
	FinishedAt  *time.Time           `json:"finishedAt,omitempty"`
}

type CatalogImportPublishInput struct {
	CompanyID   string
	StartedByID string
	SourceRef   string
	Rows        []CatalogImportRow
}

type CatalogImportFailure struct {
	CompanyID   string
	StartedByID string
	SourceRef   string
	RowCount    int
	Errors      []CatalogImportError
}
