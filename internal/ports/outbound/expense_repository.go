package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type ExpenseRepository interface {
	ListExpenses(ctx context.Context, filter domain.ExpenseFilter) ([]domain.Expense, error)
	CreateExpense(ctx context.Context, draft domain.ExpenseDraft, actor domain.ServiceActor) (domain.Expense, error)
	UpdateExpense(ctx context.Context, serviceID, expenseID string, patch domain.ExpensePatch, actor domain.ServiceActor) (domain.Expense, error)
	DeleteExpense(ctx context.Context, serviceID, expenseID string, actor domain.ServiceActor) error
}
