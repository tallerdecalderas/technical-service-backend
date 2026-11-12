package application

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrSettlementForbidden = errors.New("settlement operation forbidden")
	ErrInvalidSettlement   = errors.New("invalid settlement input")
	ErrSettlementNotFound  = outbound.ErrRecordNotFound
	ErrSettlementConflict  = outbound.ErrConflict
	ErrSettlementRule      = errors.New("settlement rule violation")
)

const (
	maxSettlementLabelRunes   = 200
	maxSettlementConceptRunes = 200
	defaultSettlementRate     = "0.5"
)

type SettlementUseCases struct {
	repository outbound.SettlementRepository
}

type SettlementListInput struct {
	TechnicianID string
	CompanyID    string
	Status       string
	DateFrom     string
	DateTo       string
	Page         int
	PageSize     int
}

type SettlementAdjustmentInput struct {
	Kind    string
	Concept string
	Amount  string
}

type SettlementGenerateInput struct {
	TechnicianID string
	CompanyID    string
	StartDate    string
	EndDate      string
	Label        string
	CompanyRate  string
	Insurance    []SettlementAdjustmentInput
	Adjustments  []SettlementAdjustmentInput
}

func NewSettlementUseCases(repository outbound.SettlementRepository) *SettlementUseCases {
	return &SettlementUseCases{repository: repository}
}

func (s *SettlementUseCases) List(ctx context.Context, user domain.User, input SettlementListInput) (domain.SettlementPage, error) {
	actor, err := settlementActor(user)
	if err != nil {
		return domain.SettlementPage{}, err
	}
	page, pageSize, err := normalizeCatalogPagination(input.Page, input.PageSize)
	if err != nil {
		return domain.SettlementPage{}, ErrInvalidSettlement
	}
	status := strings.ToUpper(strings.TrimSpace(input.Status))
	if status != "" && status != domain.SettlementStatusPending && status != domain.SettlementStatusLiquidated {
		return domain.SettlementPage{}, ErrInvalidSettlement
	}
	if actor.Role == "TECHNICIAN" {
		input.TechnicianID = actor.UserID
	} else {
		if input.TechnicianID != "" && !validUUID(input.TechnicianID) {
			return domain.SettlementPage{}, ErrInvalidSettlement
		}
		if input.CompanyID != "" && (!validUUID(input.CompanyID) || !containsString(actor.CompanyIDs, input.CompanyID)) {
			return domain.SettlementPage{}, ErrSettlementForbidden
		}
	}
	dateFrom, dateTo := "", ""
	if input.DateFrom != "" {
		parsed, ok := parsePeriodDate(input.DateFrom)
		if !ok {
			return domain.SettlementPage{}, ErrInvalidSettlement
		}
		dateFrom = parsed.Format(periodDateLayout)
	}
	if input.DateTo != "" {
		parsed, ok := parsePeriodDate(input.DateTo)
		if !ok {
			return domain.SettlementPage{}, ErrInvalidSettlement
		}
		dateTo = parsed.Format(periodDateLayout)
	}
	if dateFrom != "" && dateTo != "" && dateFrom > dateTo {
		return domain.SettlementPage{}, ErrInvalidSettlement
	}
	result, err := s.repository.ListSettlements(ctx, domain.SettlementFilter{
		Actor: actor, TechnicianID: input.TechnicianID, CompanyID: input.CompanyID,
		Status: status, DateFrom: dateFrom, DateTo: dateTo, Page: page, PageSize: pageSize,
	})
	if err != nil {
		return domain.SettlementPage{}, err
	}
	if result.Data == nil {
		result.Data = []domain.Settlement{}
	}
	return result, nil
}

func (s *SettlementUseCases) Get(ctx context.Context, user domain.User, id string) (domain.Settlement, error) {
	actor, err := settlementActor(user)
	if err != nil {
		return domain.Settlement{}, err
	}
	if !validUUID(id) {
		return domain.Settlement{}, ErrSettlementNotFound
	}
	return s.repository.GetSettlement(ctx, id, actor)
}

func (s *SettlementUseCases) Generate(ctx context.Context, user domain.User, input SettlementGenerateInput) (domain.Settlement, error) {
	actor, err := settlementActor(user)
	if err != nil {
		return domain.Settlement{}, err
	}
	if actor.Role != "ADMIN" {
		return domain.Settlement{}, ErrSettlementForbidden
	}
	start, ok := parsePeriodDate(input.StartDate)
	if !ok {
		return domain.Settlement{}, ErrInvalidSettlement
	}
	end, ok := parsePeriodDate(input.EndDate)
	if !ok || end.Before(start) {
		return domain.Settlement{}, ErrInvalidSettlement
	}
	if !validUUID(input.TechnicianID) {
		return domain.Settlement{}, ErrInvalidSettlement
	}
	if input.CompanyID != "" && (!validUUID(input.CompanyID) || !containsString(actor.CompanyIDs, input.CompanyID)) {
		return domain.Settlement{}, ErrInvalidSettlement
	}
	label := strings.TrimSpace(input.Label)
	if utf8.RuneCountInString(label) > maxSettlementLabelRunes {
		return domain.Settlement{}, ErrInvalidSettlement
	}
	companyRate := strings.TrimSpace(input.CompanyRate)
	if companyRate == "" {
		companyRate = defaultSettlementRate
	}
	if !validRate(companyRate) {
		return domain.Settlement{}, ErrInvalidSettlement
	}
	insurance, ok := normalizeSettlementAdjustments(input.Insurance, domain.AdjustmentInsurance)
	if !ok {
		return domain.Settlement{}, ErrInvalidSettlement
	}
	adjustments, ok := normalizeSettlementAdjustments(input.Adjustments, "")
	if !ok {
		return domain.Settlement{}, ErrInvalidSettlement
	}
	return s.repository.GenerateSettlement(ctx, domain.SettlementDraft{
		TechnicianID: input.TechnicianID, CompanyID: input.CompanyID,
		StartDate: start.Format(periodDateLayout), EndDate: end.Format(periodDateLayout),
		StartAt: start, EndAt: end.AddDate(0, 0, 1).Add(-time.Microsecond),
		Label: label, CompanyRate: companyRate, Insurance: insurance, Adjustments: adjustments,
		CreatedByID: actor.UserID,
	}, actor)
}

func (s *SettlementUseCases) Update(ctx context.Context, user domain.User, id string, patch domain.SettlementPatch) (domain.Settlement, error) {
	actor, err := settlementActor(user)
	if err != nil {
		return domain.Settlement{}, err
	}
	if actor.Role != "ADMIN" {
		return domain.Settlement{}, ErrSettlementForbidden
	}
	if !validUUID(id) {
		return domain.Settlement{}, ErrSettlementNotFound
	}
	if !patch.Label.Set && !patch.CompanyRate.Set && patch.Insurance == nil && patch.Adjustments == nil {
		return domain.Settlement{}, ErrInvalidSettlement
	}
	if patch.Label.Set {
		if patch.Label.Value == nil {
			return domain.Settlement{}, ErrInvalidSettlement
		}
		label := strings.TrimSpace(*patch.Label.Value)
		if utf8.RuneCountInString(label) > maxSettlementLabelRunes {
			return domain.Settlement{}, ErrInvalidSettlement
		}
		patch.Label.Value = &label
	}
	if patch.CompanyRate.Set {
		if patch.CompanyRate.Value == nil || !validRate(*patch.CompanyRate.Value) {
			return domain.Settlement{}, ErrInvalidSettlement
		}
		value := strings.TrimSpace(*patch.CompanyRate.Value)
		patch.CompanyRate.Value = &value
	}
	if patch.Insurance != nil {
		insurance, ok := normalizeSettlementAdjustments(toAdjustmentInputs(*patch.Insurance), domain.AdjustmentInsurance)
		if !ok {
			return domain.Settlement{}, ErrInvalidSettlement
		}
		patch.Insurance = &insurance
	}
	if patch.Adjustments != nil {
		adjustments, ok := normalizeSettlementAdjustments(toAdjustmentInputs(*patch.Adjustments), "")
		if !ok {
			return domain.Settlement{}, ErrInvalidSettlement
		}
		patch.Adjustments = &adjustments
	}
	return s.repository.UpdateSettlement(ctx, id, patch, actor)
}

func (s *SettlementUseCases) Liquidate(ctx context.Context, user domain.User, id string) (domain.Settlement, error) {
	actor, err := settlementActor(user)
	if err != nil {
		return domain.Settlement{}, err
	}
	if actor.Role != "ADMIN" {
		return domain.Settlement{}, ErrSettlementForbidden
	}
	if !validUUID(id) {
		return domain.Settlement{}, ErrSettlementNotFound
	}
	return s.repository.LiquidateSettlement(ctx, id, actor)
}

func settlementActor(user domain.User) (domain.ServiceActor, error) {
	actor, err := serviceActor(user)
	if err != nil {
		return domain.ServiceActor{}, ErrSettlementForbidden
	}
	if actor.Role != "ADMIN" && actor.Role != "TECHNICIAN" {
		return domain.ServiceActor{}, ErrSettlementForbidden
	}
	return actor, nil
}

func normalizeSettlementAdjustments(inputs []SettlementAdjustmentInput, forcedKind string) ([]domain.SettlementAdjustmentDraft, bool) {
	result := make([]domain.SettlementAdjustmentDraft, 0, len(inputs))
	for _, input := range inputs {
		kind := forcedKind
		if kind == "" {
			kind = strings.ToUpper(strings.TrimSpace(input.Kind))
			switch kind {
			case domain.AdjustmentInsurance, domain.AdjustmentOther, domain.AdjustmentDiscount, domain.AdjustmentAdvance:
			default:
				return nil, false
			}
		}
		concept := strings.TrimSpace(input.Concept)
		if concept == "" || utf8.RuneCountInString(concept) > maxSettlementConceptRunes ||
			!validPositiveDecimal(input.Amount) {
			return nil, false
		}
		result = append(result, domain.SettlementAdjustmentDraft{
			Kind: kind, Concept: concept, Amount: strings.TrimSpace(input.Amount),
		})
	}
	return result, true
}

func toAdjustmentInputs(values []domain.SettlementAdjustmentDraft) []SettlementAdjustmentInput {
	result := make([]SettlementAdjustmentInput, 0, len(values))
	for _, value := range values {
		result = append(result, SettlementAdjustmentInput{Kind: value.Kind, Concept: value.Concept, Amount: value.Amount})
	}
	return result
}
