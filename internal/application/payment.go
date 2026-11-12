package application

import (
	"context"
	"errors"
	"strings"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrPaymentForbidden = errors.New("payment operation forbidden")
	ErrInvalidPayment   = errors.New("invalid payment input")
	ErrPaymentNotFound  = outbound.ErrRecordNotFound
	ErrPaymentConflict  = outbound.ErrConflict
	ErrPaymentRule      = errors.New("payment rule violation")
)

const (
	defaultPaymentPageSize = 25
	maxPaymentPageSize     = 100
)

type PaymentUseCases struct {
	repository outbound.PaymentRepository
}

type PaymentListInput struct {
	TechnicianID string
	Page         int
	PageSize     int
}

type PaymentListResult struct {
	Data     []domain.Payment `json:"data"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
	Total    int              `json:"total"`
}

type PaymentCreateInput struct {
	ServiceID  string
	Method     string
	AmountPaid string
	DebtAmount string
}

func NewPaymentUseCases(repository outbound.PaymentRepository) *PaymentUseCases {
	return &PaymentUseCases{repository: repository}
}

func (s *PaymentUseCases) List(ctx context.Context, user domain.User, input PaymentListInput) (PaymentListResult, error) {
	actor, err := paymentActor(user)
	if err != nil {
		return PaymentListResult{}, err
	}
	if actor.Role != "ADMIN" {
		return PaymentListResult{}, ErrPaymentForbidden
	}
	page, pageSize := input.Page, input.PageSize
	if page == 0 {
		page = 1
	}
	if pageSize == 0 {
		pageSize = defaultPaymentPageSize
	}
	if page < 1 || pageSize < 1 || pageSize > maxPaymentPageSize ||
		page > int(^uint(0)>>1)/pageSize || !optionalUUID(input.TechnicianID) {
		return PaymentListResult{}, ErrInvalidPayment
	}
	payments, total, err := s.repository.ListPayments(ctx, domain.PaymentFilter{
		Actor: actor, TechnicianID: input.TechnicianID, Page: page, PageSize: pageSize,
	})
	if err != nil {
		return PaymentListResult{}, err
	}
	if payments == nil {
		payments = []domain.Payment{}
	}
	return PaymentListResult{Data: payments, Page: page, PageSize: pageSize, Total: total}, nil
}

func (s *PaymentUseCases) Get(ctx context.Context, user domain.User, id string) (domain.Payment, error) {
	actor, err := paymentActor(user)
	if err != nil {
		return domain.Payment{}, err
	}
	if !validUUID(id) {
		return domain.Payment{}, ErrPaymentNotFound
	}
	return s.repository.GetPayment(ctx, id, actor)
}

func (s *PaymentUseCases) Create(ctx context.Context, user domain.User, input PaymentCreateInput) (domain.Payment, error) {
	actor, err := paymentActor(user)
	if err != nil {
		return domain.Payment{}, err
	}
	if actor.Role != "TECHNICIAN" {
		return domain.Payment{}, ErrPaymentForbidden
	}
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if !validUUID(input.ServiceID) || !validPaymentMethod(method) ||
		!validAmount(input.AmountPaid) || !validAmount(input.DebtAmount) {
		return domain.Payment{}, ErrInvalidPayment
	}
	return s.repository.CreatePayment(ctx, domain.PaymentDraft{
		ServiceID: input.ServiceID, TechnicianID: actor.UserID, Method: method,
		AmountPaid: strings.TrimSpace(input.AmountPaid), DebtAmount: strings.TrimSpace(input.DebtAmount),
	}, actor)
}

func (s *PaymentUseCases) Update(ctx context.Context, user domain.User, id string, patch domain.PaymentPatch) (domain.Payment, error) {
	actor, err := paymentActor(user)
	if err != nil {
		return domain.Payment{}, err
	}
	if actor.Role != "ADMIN" {
		return domain.Payment{}, ErrPaymentForbidden
	}
	if !validUUID(id) || (!patch.Method.Set && !patch.AmountPaid.Set && !patch.DebtAmount.Set) {
		return domain.Payment{}, ErrInvalidPayment
	}
	if patch.Method.Set {
		if patch.Method.Value == nil {
			return domain.Payment{}, ErrInvalidPayment
		}
		method := strings.ToUpper(strings.TrimSpace(*patch.Method.Value))
		if !validPaymentMethod(method) {
			return domain.Payment{}, ErrInvalidPayment
		}
		patch.Method.Value = &method
	}
	for _, field := range []*domain.Field[string]{&patch.AmountPaid, &patch.DebtAmount} {
		if field.Set && (field.Value == nil || !validAmount(*field.Value)) {
			return domain.Payment{}, ErrInvalidPayment
		}
		if field.Set {
			value := strings.TrimSpace(*field.Value)
			field.Value = &value
		}
	}
	return s.repository.UpdatePayment(ctx, id, patch, actor)
}

func paymentActor(user domain.User) (domain.ServiceActor, error) {
	actor, err := serviceActor(user)
	if err != nil {
		return domain.ServiceActor{}, ErrPaymentForbidden
	}
	return actor, nil
}

func validPaymentMethod(value string) bool {
	switch value {
	case "CASH", "TRANSFER", "CARD", "OTHER":
		return true
	default:
		return false
	}
}
