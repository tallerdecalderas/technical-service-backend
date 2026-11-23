package domain

import "time"

type Template struct {
	ID          string    `json:"id"`
	CompanyID   string    `json:"companyId"`
	Name        string    `json:"name"`
	Content     string    `json:"content,omitempty"`
	IsActive    bool      `json:"isActive"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	CompanyName string    `json:"companyName,omitempty"`
}

type TemplateDraft struct {
	CompanyID string
	Name      string
	Content   string
}

type TemplatePatch struct {
	Name     *string
	Content  *string
	IsActive *bool
}

type TemplateFilter struct {
	Actor     User
	CompanyID string
	Search    string
	Page      int
	PageSize  int
}

type TemplatePage struct {
	Data     []Template
	Page     int
	PageSize int
	Total    int
}
