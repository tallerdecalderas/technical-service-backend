package application

import (
	"context"
	"errors"
	"testing"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type clientRepositoryStub struct {
	listCalls   int
	createCalls int
	filter      outbound.ClientFilter
	clients     []domain.Client
	total       int
}

func (r *clientRepositoryStub) ListClients(_ context.Context, filter outbound.ClientFilter) ([]domain.Client, int, error) {
	r.listCalls++
	r.filter = filter
	return r.clients, r.total, nil
}

func (*clientRepositoryStub) GetClient(context.Context, string, outbound.ClientFilter) (domain.Client, []domain.ClientServiceRecord, error) {
	return domain.Client{}, nil, outbound.ErrRecordNotFound
}

func (r *clientRepositoryStub) CreateClient(_ context.Context, client domain.ClientDraft) (domain.Client, error) {
	r.createCalls++
	return domain.Client{Phone: client.Phone}, nil
}

func (*clientRepositoryStub) UpdateClient(context.Context, string, domain.ClientPatch, outbound.ClientFilter) (domain.Client, error) {
	return domain.Client{}, outbound.ErrRecordNotFound
}

func adminUser() domain.User {
	return domain.User{ID: "admin-id", Role: "ADMIN", Active: true}
}

func TestClientUseCasesListAppliesDefaultsAndRequiresAdmin(t *testing.T) {
	repository := &clientRepositoryStub{clients: []domain.Client{}, total: 0}
	useCases := NewClientUseCases(repository)

	result, err := useCases.List(context.Background(), adminUser(), ClientListInput{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if repository.listCalls != 1 {
		t.Fatalf("ListClients calls = %d, want 1", repository.listCalls)
	}
	if repository.filter.Page != 1 || repository.filter.PageSize != 25 {
		t.Errorf("filter = %+v, want page 1 and pageSize 25", repository.filter)
	}
	if result.Total != 0 || result.Page != 1 || result.PageSize != 25 {
		t.Errorf("result = %+v, want empty first page", result)
	}

	repository.listCalls = 0
	user := adminUser()
	user.Role = "TECHNICIAN"
	_, err = useCases.List(context.Background(), user, ClientListInput{})
	if !errors.Is(err, ErrClientForbidden) {
		t.Fatalf("List() error = %v, want ErrClientForbidden", err)
	}
	if repository.listCalls != 0 {
		t.Errorf("ListClients called %d times for technician, want 0", repository.listCalls)
	}
}

func TestClientUseCasesListRejectsInvalidPageSize(t *testing.T) {
	repository := &clientRepositoryStub{}
	useCases := NewClientUseCases(repository)

	_, err := useCases.List(context.Background(), adminUser(), ClientListInput{Page: 1, PageSize: 101})
	if !errors.Is(err, ErrInvalidClientInput) {
		t.Fatalf("List() error = %v, want ErrInvalidClientInput", err)
	}
	if repository.listCalls != 0 {
		t.Errorf("ListClients called %d times for invalid input, want 0", repository.listCalls)
	}
}

func TestClientUseCasesCreateRejectsMalformedEmail(t *testing.T) {
	repository := &clientRepositoryStub{}
	useCases := NewClientUseCases(repository)

	_, err := useCases.Create(context.Background(), adminUser(), domain.ClientDraft{
		Phone: " 555-0100 ",
		Email: stringPointer("not-an-email"),
	})
	if !errors.Is(err, ErrInvalidClientInput) {
		t.Fatalf("Create() error = %v, want ErrInvalidClientInput", err)
	}
	if repository.createCalls != 0 {
		t.Errorf("CreateClient called %d times for invalid input, want 0", repository.createCalls)
	}
}

func TestClientUseCasesUpdateRejectsEmptyPatch(t *testing.T) {
	useCases := NewClientUseCases(&clientRepositoryStub{})

	_, err := useCases.Update(context.Background(), adminUser(), "285450f4-0262-4a9d-bc55-d4138c5ce843", domain.ClientPatch{})
	if !errors.Is(err, ErrInvalidClientInput) {
		t.Fatalf("Update() error = %v, want ErrInvalidClientInput", err)
	}
}

func stringPointer(value string) *string {
	return &value
}
