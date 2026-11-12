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
	ErrServicePartForbidden = errors.New("service part operation forbidden")
	ErrInvalidServicePart   = errors.New("invalid service part input")
	ErrServicePartNotFound  = outbound.ErrRecordNotFound
	ErrServicePartConflict  = outbound.ErrConflict
)

const (
	maxServicePartName = 500
	maxServicePartNota = 2000
)

type ServicePartUseCases struct {
	repository outbound.ServicePartRepository
}

type ServicePartCreateInput struct {
	ServiceID       string
	InventoryItemID *string
	Name            string
	Nota            *string
	Quantity        string
	UnitCost        string
	UnitSalePrice   string
}

func NewServicePartUseCases(repository outbound.ServicePartRepository) *ServicePartUseCases {
	return &ServicePartUseCases{repository: repository}
}

func (s *ServicePartUseCases) List(ctx context.Context, user domain.User, serviceID string) ([]domain.ServicePart, error) {
	actor, err := servicePartActor(user)
	if err != nil {
		return nil, err
	}
	if !validUUID(serviceID) {
		return nil, ErrServicePartNotFound
	}
	parts, err := s.repository.ListServiceParts(ctx, serviceID, actor)
	if err != nil {
		return nil, err
	}
	if parts == nil {
		return []domain.ServicePart{}, nil
	}
	return parts, nil
}

func (s *ServicePartUseCases) Create(ctx context.Context, user domain.User, input ServicePartCreateInput) (domain.ServicePart, error) {
	actor, err := servicePartActor(user)
	if err != nil {
		return domain.ServicePart{}, err
	}
	if actor.Role != "ADMIN" {
		return domain.ServicePart{}, ErrServicePartForbidden
	}
	if !validServicePartCreate(input) {
		return domain.ServicePart{}, ErrInvalidServicePart
	}
	return s.repository.CreateServicePart(ctx, domain.ServicePartDraft{
		ServiceID: input.ServiceID, InventoryItemID: input.InventoryItemID,
		Name: strings.TrimSpace(input.Name), Nota: normalizeServicePartNota(input.Nota),
		Quantity: strings.TrimSpace(input.Quantity), UnitCost: strings.TrimSpace(input.UnitCost),
		UnitSalePrice: strings.TrimSpace(input.UnitSalePrice),
	}, actor)
}

func (s *ServicePartUseCases) Update(ctx context.Context, user domain.User, serviceID, partID string, patch domain.ServicePartPatch) (domain.ServicePart, error) {
	actor, err := servicePartActor(user)
	if err != nil {
		return domain.ServicePart{}, err
	}
	if actor.Role != "ADMIN" {
		return domain.ServicePart{}, ErrServicePartForbidden
	}
	if !validUUID(serviceID) || !validUUID(partID) || !validServicePartPatch(patch) {
		return domain.ServicePart{}, ErrInvalidServicePart
	}
	if patch.Name.Set && patch.Name.Value != nil {
		value := strings.TrimSpace(*patch.Name.Value)
		patch.Name.Value = &value
	}
	for _, field := range []*domain.Field[string]{&patch.Quantity, &patch.UnitCost, &patch.UnitSalePrice} {
		if field.Set && field.Value != nil {
			value := strings.TrimSpace(*field.Value)
			field.Value = &value
		}
	}
	if patch.Nota.Set {
		patch.Nota.Value = normalizeServicePartNota(patch.Nota.Value)
	}
	return s.repository.UpdateServicePart(ctx, serviceID, partID, patch, actor)
}

func (s *ServicePartUseCases) Delete(ctx context.Context, user domain.User, serviceID, partID string) error {
	actor, err := servicePartActor(user)
	if err != nil {
		return err
	}
	if actor.Role != "ADMIN" {
		return ErrServicePartForbidden
	}
	if !validUUID(serviceID) || !validUUID(partID) {
		return ErrServicePartNotFound
	}
	return s.repository.DeleteServicePart(ctx, serviceID, partID, actor)
}

func servicePartActor(user domain.User) (domain.ServiceActor, error) {
	actor, err := serviceActor(user)
	if err != nil {
		return domain.ServiceActor{}, ErrServicePartForbidden
	}
	return actor, nil
}

func validServicePartCreate(input ServicePartCreateInput) bool {
	if !validUUID(input.ServiceID) || input.InventoryItemID != nil && !validUUID(*input.InventoryItemID) ||
		strings.TrimSpace(input.Name) == "" || !validPositiveAmount(input.Quantity) ||
		!validAmount(input.UnitCost) || !validAmount(input.UnitSalePrice) {
		return false
	}
	return utf8.RuneCountInString(strings.TrimSpace(input.Name)) <= maxServicePartName &&
		validServicePartNota(input.Nota)
}

func validServicePartPatch(patch domain.ServicePartPatch) bool {
	if !patch.Name.Set && !patch.Nota.Set && !patch.Quantity.Set && !patch.UnitCost.Set && !patch.UnitSalePrice.Set {
		return false
	}
	if patch.Name.Set && (patch.Name.Value == nil || strings.TrimSpace(*patch.Name.Value) == "" ||
		utf8.RuneCountInString(strings.TrimSpace(*patch.Name.Value)) > maxServicePartName) {
		return false
	}
	if !validServicePartNota(patch.Nota.Value) {
		return false
	}
	if patch.Quantity.Set && (patch.Quantity.Value == nil || !validPositiveAmount(*patch.Quantity.Value)) {
		return false
	}
	for _, field := range []domain.Field[string]{patch.UnitCost, patch.UnitSalePrice} {
		if field.Set && (field.Value == nil || !validAmount(*field.Value)) {
			return false
		}
	}
	return true
}

func validServicePartNota(value *string) bool {
	return value == nil || utf8.RuneCountInString(strings.TrimSpace(*value)) <= maxServicePartNota
}

func normalizeServicePartNota(value *string) *string {
	return normalizeNullableString(value)
}
