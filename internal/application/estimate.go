package application

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrEstimateForbidden = errors.New("estimate operation forbidden")
	ErrInvalidEstimate   = errors.New("invalid estimate input")
	ErrEstimateNotFound  = outbound.ErrRecordNotFound
	ErrEstimateConflict  = outbound.ErrConflict
	ErrEstimateRule      = errors.New("estimate rule violation")
)

const (
	defaultEstimatePageSize = 25
	maxEstimatePageSize     = 100
	maxEstimateDescription  = 2000
	maxEstimateNotes        = 4000
)

type EstimateUseCases struct {
	repository outbound.EstimateRepository
}

type EstimateListInput struct {
	Status       string
	TechnicianID string
	Page         int
	PageSize     int
}

type EstimateListResult struct {
	Data     []domain.Estimate `json:"data"`
	Page     int               `json:"page"`
	PageSize int               `json:"pageSize"`
	Total    int               `json:"total"`
}

type EstimateCreateInput struct {
	ServiceID   string
	Amount      string
	Description *string
	Notes       *string
}

func NewEstimateUseCases(repository outbound.EstimateRepository) *EstimateUseCases {
	return &EstimateUseCases{repository: repository}
}

func (s *EstimateUseCases) List(ctx context.Context, user domain.User, input EstimateListInput) (EstimateListResult, error) {
	actor, err := estimateActor(user)
	if err != nil {
		return EstimateListResult{}, err
	}
	page, pageSize := input.Page, input.PageSize
	if page == 0 {
		page = 1
	}
	if pageSize == 0 {
		pageSize = defaultEstimatePageSize
	}
	status := strings.ToUpper(strings.TrimSpace(input.Status))
	if page < 1 || pageSize < 1 || pageSize > maxEstimatePageSize ||
		page > int(^uint(0)>>1)/pageSize || !validEstimateStatus(status) ||
		!optionalUUID(input.TechnicianID) {
		return EstimateListResult{}, ErrInvalidEstimate
	}
	if actor.Role == "TECHNICIAN" && input.TechnicianID != "" && !strings.EqualFold(input.TechnicianID, actor.UserID) {
		return EstimateListResult{}, ErrEstimateForbidden
	}
	estimates, total, err := s.repository.ListEstimates(ctx, domain.EstimateFilter{
		Actor: actor, Status: status, TechnicianID: input.TechnicianID, Page: page, PageSize: pageSize,
	})
	if err != nil {
		return EstimateListResult{}, err
	}
	if estimates == nil {
		estimates = []domain.Estimate{}
	}
	return EstimateListResult{Data: estimates, Page: page, PageSize: pageSize, Total: total}, nil
}

func (s *EstimateUseCases) Get(ctx context.Context, user domain.User, id string) (domain.Estimate, error) {
	actor, err := estimateActor(user)
	if err != nil {
		return domain.Estimate{}, err
	}
	if !validUUID(id) {
		return domain.Estimate{}, ErrEstimateNotFound
	}
	return s.repository.GetEstimate(ctx, id, actor)
}

func (s *EstimateUseCases) Create(ctx context.Context, user domain.User, input EstimateCreateInput) (domain.Estimate, error) {
	actor, err := estimateActor(user)
	if err != nil {
		return domain.Estimate{}, err
	}
	if actor.Role != "TECHNICIAN" {
		return domain.Estimate{}, ErrEstimateForbidden
	}
	if !validUUID(input.ServiceID) || !validPositiveAmount(input.Amount) {
		return domain.Estimate{}, ErrInvalidEstimate
	}
	description := normalizeNullableString(input.Description)
	notes := normalizeNullableString(input.Notes)
	if (description != nil && utf8.RuneCountInString(*description) > maxEstimateDescription) ||
		(notes != nil && utf8.RuneCountInString(*notes) > maxEstimateNotes) {
		return domain.Estimate{}, ErrInvalidEstimate
	}
	return s.repository.CreateEstimate(ctx, domain.EstimateDraft{
		ServiceID: input.ServiceID, Amount: strings.TrimSpace(input.Amount), Description: description, Notes: notes,
	}, actor)
}

func (s *EstimateUseCases) Finalize(ctx context.Context, user domain.User, id string) (domain.Estimate, error) {
	actor, err := estimateActor(user)
	if err != nil {
		return domain.Estimate{}, err
	}
	if actor.Role != "ADMIN" {
		return domain.Estimate{}, ErrEstimateForbidden
	}
	if !validUUID(id) {
		return domain.Estimate{}, ErrEstimateNotFound
	}
	return s.repository.FinalizeEstimate(ctx, id, actor)
}

func estimateActor(user domain.User) (domain.ServiceActor, error) {
	actor, err := serviceActor(user)
	if err != nil {
		return domain.ServiceActor{}, ErrEstimateForbidden
	}
	return actor, nil
}

func validEstimateStatus(value string) bool {
	return value == "" || value == "PENDING" || value == "FINALIZED"
}

func validPositiveAmount(value string) bool {
	if !validAmount(value) {
		return false
	}
	for _, character := range value {
		if character >= '1' && character <= '9' {
			return true
		}
	}
	return false
}
