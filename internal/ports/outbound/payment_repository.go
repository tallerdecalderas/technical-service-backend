package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type PaymentRepository interface {
	ListPayments(context.Context, domain.PaymentFilter) ([]domain.Payment, int, error)
	GetPayment(context.Context, string, domain.ServiceActor) (domain.Payment, error)
	CreatePayment(context.Context, domain.PaymentDraft, domain.ServiceActor) (domain.Payment, error)
	UpdatePayment(context.Context, string, domain.PaymentPatch, domain.ServiceActor) (domain.Payment, error)
}
