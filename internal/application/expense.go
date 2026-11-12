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
	ErrExpenseForbidden = errors.New("expense operation forbidden")
	ErrInvalidExpense   = errors.New("invalid expense input")
	ErrExpenseNotFound  = outbound.ErrRecordNotFound
	ErrExpenseConflict  = outbound.ErrConflict
)

const (
	maxExpenseConceptRunes = 500
)

type ExpenseUseCases struct {
	repository outbound.ExpenseRepository
}

type ExpenseCreateInput struct {
	ServiceID string
	Concept   string
	Amount    string
	Currency  string
}

func NewExpenseUseCases(repository outbound.ExpenseRepository) *ExpenseUseCases {
	return &ExpenseUseCases{repository: repository}
}

func (s *ExpenseUseCases) List(ctx context.Context, user domain.User, serviceID string) ([]domain.Expense, error) {
	actor, err := expenseActor(user)
	if err != nil {
		return nil, err
	}
	if !validUUID(serviceID) {
		return nil, ErrInvalidExpense
	}
	expenses, err := s.repository.ListExpenses(ctx, domain.ExpenseFilter{
		Actor: actor, ServiceID: serviceID,
	})
	if err != nil {
		return nil, err
	}
	if expenses == nil {
		return []domain.Expense{}, nil
	}
	return expenses, nil
}

func (s *ExpenseUseCases) Create(ctx context.Context, user domain.User, input ExpenseCreateInput) (domain.Expense, error) {
	actor, err := expenseActor(user)
	if err != nil {
		return domain.Expense{}, err
	}
	concept, ok := normalizeExpenseConcept(input.Concept)
	if !ok || !validUUID(input.ServiceID) || !validPositiveAmount(input.Amount) {
		return domain.Expense{}, ErrInvalidExpense
	}
	currency, ok := normalizeExpenseCurrency(input.Currency)
	if !ok {
		return domain.Expense{}, ErrInvalidExpense
	}
	return s.repository.CreateExpense(ctx, domain.ExpenseDraft{
		ServiceID: input.ServiceID, Concept: concept, Amount: strings.TrimSpace(input.Amount),
		Currency: currency, RecordedByID: actor.UserID,
	}, actor)
}

func (s *ExpenseUseCases) Update(ctx context.Context, user domain.User, serviceID, expenseID string, patch domain.ExpensePatch) (domain.Expense, error) {
	actor, err := expenseActor(user)
	if err != nil {
		return domain.Expense{}, err
	}
	if actor.Role != "ADMIN" {
		return domain.Expense{}, ErrExpenseForbidden
	}
	if !validUUID(serviceID) || !validUUID(expenseID) ||
		(!patch.Concept.Set && !patch.Amount.Set && !patch.Currency.Set) {
		return domain.Expense{}, ErrInvalidExpense
	}
	if patch.Concept.Set {
		if patch.Concept.Value == nil {
			return domain.Expense{}, ErrInvalidExpense
		}
		concept, ok := normalizeExpenseConcept(*patch.Concept.Value)
		if !ok {
			return domain.Expense{}, ErrInvalidExpense
		}
		patch.Concept.Value = &concept
	}
	if patch.Amount.Set {
		if patch.Amount.Value == nil || !validPositiveAmount(*patch.Amount.Value) {
			return domain.Expense{}, ErrInvalidExpense
		}
		value := strings.TrimSpace(*patch.Amount.Value)
		patch.Amount.Value = &value
	}
	if patch.Currency.Set {
		if patch.Currency.Value == nil {
			return domain.Expense{}, ErrInvalidExpense
		}
		currency, ok := normalizeExpenseCurrency(*patch.Currency.Value)
		if !ok {
			return domain.Expense{}, ErrInvalidExpense
		}
		patch.Currency.Value = &currency
	}
	return s.repository.UpdateExpense(ctx, serviceID, expenseID, patch, actor)
}

func (s *ExpenseUseCases) Delete(ctx context.Context, user domain.User, serviceID, expenseID string) error {
	actor, err := expenseActor(user)
	if err != nil {
		return err
	}
	if actor.Role != "ADMIN" {
		return ErrExpenseForbidden
	}
	if !validUUID(serviceID) || !validUUID(expenseID) {
		return ErrExpenseNotFound
	}
	return s.repository.DeleteExpense(ctx, serviceID, expenseID, actor)
}

func expenseActor(user domain.User) (domain.ServiceActor, error) {
	actor, err := serviceActor(user)
	if err != nil {
		return domain.ServiceActor{}, ErrExpenseForbidden
	}
	if actor.Role != "ADMIN" && actor.Role != "TECHNICIAN" {
		return domain.ServiceActor{}, ErrExpenseForbidden
	}
	return actor, nil
}

func normalizeExpenseConcept(value string) (string, bool) {
	concept := strings.TrimSpace(value)
	if concept == "" || utf8.RuneCountInString(concept) > maxExpenseConceptRunes {
		return "", false
	}
	return concept, true
}

func normalizeExpenseCurrency(value string) (string, bool) {
	currency := strings.ToUpper(strings.TrimSpace(value))
	if currency != "ARS" && currency != "USD" {
		return "", false
	}
	return currency, true
}
