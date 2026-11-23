package application

import (
	"context"
	"errors"
	"strings"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrTemplateForbidden = errors.New("template operation forbidden")
	ErrInvalidTemplate   = errors.New("invalid template input")
)

type TemplateUseCases struct {
	repository outbound.TemplateRepository
}

func NewTemplateUseCases(repository outbound.TemplateRepository) *TemplateUseCases {
	return &TemplateUseCases{repository: repository}
}

func (s *TemplateUseCases) Create(ctx context.Context, user domain.User, input domain.TemplateDraft) (string, error) {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return "", ErrTemplateForbidden
	}
	if strings.TrimSpace(input.Name) == "" {
		return "", ErrInvalidTemplate
	}
	return s.repository.CreateTemplate(ctx, input)
}

func (s *TemplateUseCases) Get(ctx context.Context, user domain.User, id string) (*domain.Template, error) {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return nil, ErrTemplateForbidden
	}
	if id == "" {
		return nil, errors.New("template ID is required")
	}
	return s.repository.GetTemplate(ctx, id)
}

func (s *TemplateUseCases) List(ctx context.Context, user domain.User, companyID string, filter domain.TemplateFilter) (domain.TemplatePage, error) {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return domain.TemplatePage{}, ErrTemplateForbidden
	}
	if companyID == "" {
		return domain.TemplatePage{}, errors.New("company ID is required")
	}
	return s.repository.ListTemplates(ctx, companyID, filter)
}

func (s *TemplateUseCases) Update(ctx context.Context, user domain.User, id string, input domain.TemplatePatch) error {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return ErrTemplateForbidden
	}
	if id == "" {
		return errors.New("template ID is required")
	}
	return s.repository.UpdateTemplate(ctx, id, input)
}

func (s *TemplateUseCases) Deactivate(ctx context.Context, user domain.User, id string) error {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return ErrTemplateForbidden
	}
	if id == "" {
		return errors.New("template ID is required")
	}
	return s.repository.DeleteTemplate(ctx, id)
}
