package application

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"unicode/utf8"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrClientForbidden    = errors.New("client operation forbidden")
	ErrInvalidClientInput = errors.New("invalid client input")
	ErrClientNotFound     = outbound.ErrRecordNotFound
	ErrClientConflict     = outbound.ErrConflict
)

const (
	defaultClientPageSize = 25
	maxClientPageSize     = 100
	maxClientSearchLength = 120
)

type ClientUseCases struct {
	repository outbound.ClientRepository
}

type ClientListInput struct {
	Search   string
	Page     int
	PageSize int
}

type ClientListResult struct {
	Data     []domain.Client `json:"data"`
	Page     int             `json:"page"`
	PageSize int             `json:"pageSize"`
	Total    int             `json:"total"`
}

func NewClientUseCases(repository outbound.ClientRepository) *ClientUseCases {
	return &ClientUseCases{repository: repository}
}

func (s *ClientUseCases) List(ctx context.Context, user domain.User, input ClientListInput) (ClientListResult, error) {
	if err := requireClientAdmin(user); err != nil {
		return ClientListResult{}, err
	}
	page := input.Page
	if page == 0 {
		page = 1
	}
	pageSize := input.PageSize
	if pageSize == 0 {
		pageSize = defaultClientPageSize
	}
	search := strings.TrimSpace(input.Search)
	if page < 1 || pageSize < 1 || pageSize > maxClientPageSize || utf8.RuneCountInString(search) > maxClientSearchLength {
		return ClientListResult{}, ErrInvalidClientInput
	}
	if page > int(^uint(0)>>1)/pageSize {
		return ClientListResult{}, ErrInvalidClientInput
	}
	clients, total, err := s.repository.ListClients(ctx, clientScope(user, outbound.ClientFilter{
		Search: search, Page: page, PageSize: pageSize,
	}))
	if err != nil {
		return ClientListResult{}, err
	}
	if clients == nil {
		clients = []domain.Client{}
	}
	return ClientListResult{Data: clients, Page: page, PageSize: pageSize, Total: total}, nil
}

func (s *ClientUseCases) Get(ctx context.Context, user domain.User, id string) (domain.Client, error) {
	if err := requireClientReader(user); err != nil {
		return domain.Client{}, err
	}
	if !validUUID(id) {
		return domain.Client{}, ErrClientNotFound
	}
	client, services, err := s.repository.GetClient(ctx, id, clientScope(user, outbound.ClientFilter{}))
	if err != nil {
		return domain.Client{}, err
	}
	client.Services = services
	return client, nil
}

func (s *ClientUseCases) Create(ctx context.Context, user domain.User, draft domain.ClientDraft) (domain.Client, error) {
	if err := requireClientAdmin(user); err != nil {
		return domain.Client{}, err
	}
	draft, err := normalizeClientDraft(draft)
	if err != nil {
		return domain.Client{}, err
	}
	return s.repository.CreateClient(ctx, draft)
}

func (s *ClientUseCases) Update(ctx context.Context, user domain.User, id string, patch domain.ClientPatch) (domain.Client, error) {
	if err := requireClientAdmin(user); err != nil {
		return domain.Client{}, err
	}
	if !validUUID(id) {
		return domain.Client{}, ErrClientNotFound
	}
	patch, err := normalizeClientPatch(patch)
	if err != nil {
		return domain.Client{}, err
	}
	return s.repository.UpdateClient(ctx, id, patch, clientScope(user, outbound.ClientFilter{}))
}

func requireClientAdmin(user domain.User) error {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return ErrClientForbidden
	}
	return nil
}

func requireClientReader(user domain.User) error {
	if !user.Active || (!strings.EqualFold(user.Role, "ADMIN") && !strings.EqualFold(user.Role, "TECHNICIAN")) {
		return ErrClientForbidden
	}
	return nil
}

func clientScope(user domain.User, filter outbound.ClientFilter) outbound.ClientFilter {
	filter.UserID = user.ID
	filter.Role = strings.ToUpper(user.Role)
	filter.CompanyIDs = append([]string(nil), user.CompanyIDs...)
	return filter
}

func normalizeClientDraft(draft domain.ClientDraft) (domain.ClientDraft, error) {
	draft.Name = normalizeNullableString(draft.Name)
	draft.Email = normalizeNullableString(draft.Email)
	draft.Address = normalizeNullableString(draft.Address)
	draft.Locality = normalizeNullableString(draft.Locality)
	draft.Phone = strings.TrimSpace(draft.Phone)
	if draft.Phone == "" || !validEmail(draft.Email) {
		return domain.ClientDraft{}, ErrInvalidClientInput
	}
	return draft, nil
}

func normalizeClientPatch(patch domain.ClientPatch) (domain.ClientPatch, error) {
	if !patch.Name.Set && !patch.Email.Set && !patch.Phone.Set && !patch.Address.Set && !patch.Locality.Set {
		return domain.ClientPatch{}, ErrInvalidClientInput
	}
	for _, field := range []*domain.ClientField{&patch.Name, &patch.Email, &patch.Address, &patch.Locality} {
		if field.Set {
			field.Value = normalizeNullableString(field.Value)
		}
	}
	if patch.Phone.Set {
		if patch.Phone.Value == nil {
			return domain.ClientPatch{}, ErrInvalidClientInput
		}
		phone := strings.TrimSpace(*patch.Phone.Value)
		if phone == "" {
			return domain.ClientPatch{}, ErrInvalidClientInput
		}
		patch.Phone.Value = &phone
	}
	if patch.Email.Set && !validEmail(patch.Email.Value) {
		return domain.ClientPatch{}, ErrInvalidClientInput
	}
	return patch, nil
}

func normalizeNullableString(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func validEmail(value *string) bool {
	if value == nil {
		return true
	}
	address, err := mail.ParseAddress(*value)
	return err == nil && address.Address == *value
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}
