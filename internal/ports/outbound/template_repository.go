package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type TemplateRepository interface {
	CreateTemplate(ctx context.Context, input domain.TemplateDraft) (string, error)
	GetTemplate(ctx context.Context, id string) (*domain.Template, error)
	ListTemplates(ctx context.Context, companyID string, filter domain.TemplateFilter) (domain.TemplatePage, error)
	UpdateTemplate(ctx context.Context, id string, input domain.TemplatePatch) error
	DeleteTemplate(ctx context.Context, id string) error
}
