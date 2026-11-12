package application

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrServiceForbidden = errors.New("service operation forbidden")
	ErrInvalidService   = errors.New("invalid service input")
	ErrServiceNotFound  = outbound.ErrRecordNotFound
	ErrServiceConflict  = outbound.ErrConflict
	ErrServiceRule      = errors.New("service rule violation")
)

const (
	defaultServicePageSize = 25
	maxServicePageSize     = 100
)

var decimalAmountPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,6})?$`)

type ServiceUseCases struct {
	repository outbound.ServiceRepository
}

type ServiceListInput struct {
	Status       string
	TechnicianID string
	ClientID     string
	CompanyID    string
	CategoryID   string
	DateFrom     *time.Time
	DateTo       *time.Time
	Page         int
	PageSize     int
}

type ServiceListResult struct {
	Data     []domain.Service `json:"data"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
	Total    int              `json:"total"`
}

type ServiceCreateInput struct {
	CompanyID      string
	ClientID       string
	CategoryID     string
	TechnicianID   *string
	ScheduledDate  time.Time
	Address        string
	Locality       string
	Observation    *string
	ExpectedAmount *string
	FinalAmount    *string
}

func NewServiceUseCases(repository outbound.ServiceRepository) *ServiceUseCases {
	return &ServiceUseCases{repository: repository}
}

func (s *ServiceUseCases) List(ctx context.Context, user domain.User, input ServiceListInput) (ServiceListResult, error) {
	actor, err := serviceActor(user)
	if err != nil {
		return ServiceListResult{}, err
	}
	page := input.Page
	if page == 0 {
		page = 1
	}
	pageSize := input.PageSize
	if pageSize == 0 {
		pageSize = defaultServicePageSize
	}
	if page < 1 || pageSize < 1 || pageSize > maxServicePageSize ||
		page > int(^uint(0)>>1)/pageSize || !validServiceStatus(input.Status, true) ||
		!optionalUUID(input.TechnicianID) || !optionalUUID(input.ClientID) ||
		!optionalUUID(input.CompanyID) || !optionalUUID(input.CategoryID) ||
		input.DateFrom != nil && input.DateTo != nil && input.DateTo.Before(*input.DateFrom) {
		return ServiceListResult{}, ErrInvalidService
	}
	if strings.EqualFold(user.Role, "TECHNICIAN") && input.TechnicianID != "" && input.TechnicianID != user.ID {
		return ServiceListResult{}, ErrServiceForbidden
	}
	if input.CompanyID != "" && !containsString(actor.CompanyIDs, input.CompanyID) {
		return ServiceListResult{}, ErrServiceForbidden
	}
	filter := domain.ServiceFilter{
		Actor: actor, Status: input.Status, TechnicianID: input.TechnicianID,
		ClientID: input.ClientID, CompanyID: input.CompanyID, CategoryID: input.CategoryID,
		DateFrom: input.DateFrom, DateTo: input.DateTo, Page: page, PageSize: pageSize,
	}
	services, total, err := s.repository.ListServices(ctx, filter)
	if err != nil {
		return ServiceListResult{}, err
	}
	if services == nil {
		services = []domain.Service{}
	}
	return ServiceListResult{Data: services, Page: page, PageSize: pageSize, Total: total}, nil
}

func (s *ServiceUseCases) Get(ctx context.Context, user domain.User, id string) (domain.Service, error) {
	actor, err := serviceActor(user)
	if err != nil {
		return domain.Service{}, err
	}
	if !validUUID(id) {
		return domain.Service{}, ErrServiceNotFound
	}
	return s.repository.GetService(ctx, id, actor)
}

func (s *ServiceUseCases) History(ctx context.Context, user domain.User, id string, page, pageSize int) (domain.ServiceHistoryResult, error) {
	actor, err := serviceActor(user)
	if err != nil {
		return domain.ServiceHistoryResult{}, err
	}
	if !validUUID(id) {
		return domain.ServiceHistoryResult{}, ErrServiceNotFound
	}
	if page == 0 {
		page = 1
	}
	if pageSize == 0 {
		pageSize = defaultServicePageSize
	}
	if page < 1 || pageSize < 1 || pageSize > maxServicePageSize ||
		page > int(^uint(0)>>1)/pageSize {
		return domain.ServiceHistoryResult{}, ErrInvalidService
	}
	data, total, err := s.repository.ListServiceHistory(ctx, id, actor, page, pageSize)
	if err != nil {
		return domain.ServiceHistoryResult{}, err
	}
	if data == nil {
		data = []domain.ActivityLog{}
	}
	return domain.ServiceHistoryResult{Data: data, Page: page, PageSize: pageSize, Total: total}, nil
}

func (s *ServiceUseCases) Create(ctx context.Context, user domain.User, input ServiceCreateInput) (domain.Service, error) {
	actor, err := serviceActor(user)
	if err != nil {
		return domain.Service{}, err
	}
	if !strings.EqualFold(user.Role, "ADMIN") {
		return domain.Service{}, ErrServiceForbidden
	}
	if !validUUID(input.CompanyID) || !containsString(actor.CompanyIDs, input.CompanyID) ||
		!validUUID(input.ClientID) || !validUUID(input.CategoryID) ||
		input.TechnicianID != nil && !validUUID(*input.TechnicianID) ||
		input.ScheduledDate.IsZero() || strings.TrimSpace(input.Address) == "" ||
		strings.TrimSpace(input.Locality) == "" || !validAmountPointer(input.ExpectedAmount) ||
		input.FinalAmount != nil && !validAmount(*input.FinalAmount) {
		return domain.Service{}, ErrInvalidService
	}
	address, locality := strings.TrimSpace(input.Address), strings.TrimSpace(input.Locality)
	if utf8.RuneCountInString(address) > 500 || utf8.RuneCountInString(locality) > 160 {
		return domain.Service{}, ErrInvalidService
	}
	input.Observation = normalizeNullableString(input.Observation)
	finalAmount := "0"
	if input.FinalAmount != nil {
		finalAmount = strings.TrimSpace(*input.FinalAmount)
	}
	return s.repository.CreateService(ctx, domain.ServiceDraft{
		CompanyID: input.CompanyID, ClientID: input.ClientID, CategoryID: input.CategoryID,
		TechnicianID: input.TechnicianID, CreatedByID: user.ID, ScheduledDate: input.ScheduledDate.UTC(),
		Address: address, Locality: locality, Observation: input.Observation,
		ExpectedAmount: normalizeNullableString(input.ExpectedAmount), FinalAmount: finalAmount,
	})
}

func (s *ServiceUseCases) Update(ctx context.Context, user domain.User, id string, patch domain.ServicePatch) (domain.Service, error) {
	actor, err := serviceActor(user)
	if err != nil {
		return domain.Service{}, err
	}
	if !strings.EqualFold(user.Role, "ADMIN") {
		return domain.Service{}, ErrServiceForbidden
	}
	if !validUUID(id) {
		return domain.Service{}, ErrServiceNotFound
	}
	if !patch.CompanyID.Set && !patch.ClientID.Set && !patch.CategoryID.Set && !patch.TechnicianID.Set &&
		!patch.ScheduledDate.Set && !patch.Address.Set && !patch.Locality.Set &&
		!patch.Observation.Set && !patch.ExpectedAmount.Set && !patch.FinalAmount.Set {
		return domain.Service{}, ErrInvalidService
	}
	for _, field := range []*domain.Field[string]{&patch.CompanyID, &patch.ClientID, &patch.CategoryID} {
		if field.Set && (field.Value == nil || !validUUID(*field.Value)) {
			return domain.Service{}, ErrInvalidService
		}
	}
	if patch.CompanyID.Set && !containsString(actor.CompanyIDs, *patch.CompanyID.Value) {
		return domain.Service{}, ErrServiceForbidden
	}
	if patch.TechnicianID.Set && patch.TechnicianID.Value != nil && !validUUID(*patch.TechnicianID.Value) {
		return domain.Service{}, ErrInvalidService
	}
	if patch.ScheduledDate.Set && (patch.ScheduledDate.Value == nil || patch.ScheduledDate.Value.IsZero()) {
		return domain.Service{}, ErrInvalidService
	}
	for _, field := range []*domain.Field[string]{&patch.Address, &patch.Locality} {
		if field.Set && (field.Value == nil || strings.TrimSpace(*field.Value) == "") {
			return domain.Service{}, ErrInvalidService
		}
		if field.Set && field.Value != nil {
			normalized := strings.TrimSpace(*field.Value)
			maxLength := 500
			if field == &patch.Locality {
				maxLength = 160
			}
			if utf8.RuneCountInString(normalized) > maxLength {
				return domain.Service{}, ErrInvalidService
			}
			field.Value = &normalized
		}
	}
	if patch.Observation.Set && patch.Observation.Value != nil {
		patch.Observation.Value = normalizeNullableString(patch.Observation.Value)
	}
	if patch.ExpectedAmount.Set && !validAmountPointer(patch.ExpectedAmount.Value) {
		return domain.Service{}, ErrInvalidService
	}
	if patch.FinalAmount.Set && (patch.FinalAmount.Value == nil || !validAmount(*patch.FinalAmount.Value)) {
		return domain.Service{}, ErrInvalidService
	}
	return s.repository.UpdateService(ctx, id, patch, actor)
}

func (s *ServiceUseCases) ChangeStatus(ctx context.Context, user domain.User, id, status string) (domain.Service, error) {
	actor, err := serviceActor(user)
	if err != nil {
		return domain.Service{}, err
	}
	if !validUUID(id) {
		return domain.Service{}, ErrServiceNotFound
	}
	status = strings.ToUpper(strings.TrimSpace(status))
	if status != "IN_PROGRESS" && status != "CANCELLED" {
		return domain.Service{}, ErrInvalidService
	}
	if strings.EqualFold(user.Role, "TECHNICIAN") && status != "IN_PROGRESS" {
		return domain.Service{}, ErrServiceForbidden
	}
	return s.repository.ChangeServiceStatus(ctx, id, status, user.ID, actor)
}

func (s *ServiceUseCases) Finish(ctx context.Context, user domain.User, id string, input domain.ServiceFinish) (domain.Service, error) {
	actor, err := serviceActor(user)
	if err != nil {
		return domain.Service{}, err
	}
	if !strings.EqualFold(user.Role, "TECHNICIAN") {
		return domain.Service{}, ErrServiceForbidden
	}
	if !validUUID(id) || !validAmount(input.FinalAmount) {
		return domain.Service{}, ErrInvalidService
	}
	input.FinalAmount = strings.TrimSpace(input.FinalAmount)
	input.Observation = normalizeNullableString(input.Observation)
	return s.repository.FinishService(ctx, id, input, actor)
}

func serviceActor(user domain.User) (domain.ServiceActor, error) {
	role := strings.ToUpper(user.Role)
	if !user.Active || user.ID == "" || (role != "ADMIN" && role != "TECHNICIAN") {
		return domain.ServiceActor{}, ErrServiceForbidden
	}
	companyIDs, ok := authorizedCompanyIDs(user.CompanyIDs, user.CompanyIDs)
	if !ok || len(companyIDs) == 0 {
		return domain.ServiceActor{}, ErrServiceForbidden
	}
	return domain.ServiceActor{UserID: user.ID, Role: role, CompanyIDs: companyIDs}, nil
}

func validServiceStatus(status string, includeLegacy bool) bool {
	if status == "" {
		return true
	}
	switch strings.ToUpper(status) {
	case "PENDING", "IN_PROGRESS", "COMPLETED", "CANCELLED":
		return true
	case "CLOSED":
		return includeLegacy
	default:
		return false
	}
}

func validAmountPointer(value *string) bool {
	return value == nil || validAmount(*value)
}

func validAmount(value string) bool {
	return decimalAmountPattern.MatchString(strings.TrimSpace(value))
}

func optionalUUID(value string) bool {
	return value == "" || validUUID(value)
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}
