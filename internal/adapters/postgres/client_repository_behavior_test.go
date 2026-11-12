package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

func TestBuildClientUpdateQueryUsesOnlySetFields(t *testing.T) {
	query, args, err := buildClientUpdateQuery(
		"285450f4-0262-4a9d-bc55-d4138c5ce843",
		domain.ClientPatch{
			Email:   domain.ClientField{Set: true},
			Address: domain.ClientField{Set: true, Value: stringValue("Calle 1")},
		},
		outbound.ClientFilter{
			UserID: "admin-id", Role: "ADMIN", CompanyIDs: []string{"company-id"},
		},
	)
	if err != nil {
		t.Fatalf("buildClientUpdateQuery() error = %v", err)
	}
	email, emailIsPointer := (*string)(nil), false
	address, addressOK := (*string)(nil), false
	if len(args) > 0 {
		email, emailIsPointer = args[0].(*string)
	}
	if len(args) > 1 {
		address, addressOK = args[1].(*string)
	}
	if len(args) != 6 || !(args[0] == nil || (emailIsPointer && email == nil)) || !addressOK || address == nil ||
		*address != "Calle 1" || args[2] != "285450f4-0262-4a9d-bc55-d4138c5ce843" {
		t.Errorf("args = %#v, want nullable email, address and client ID", args)
	}
	if query == "" {
		t.Fatal("query is empty")
	}
}

func TestBuildClientUpdateQueryRejectsEmptyPatch(t *testing.T) {
	_, _, err := buildClientUpdateQuery("285450f4-0262-4a9d-bc55-d4138c5ce843", domain.ClientPatch{}, outbound.ClientFilter{})
	if !errors.Is(err, ErrInvalidClientPatch) {
		t.Fatalf("buildClientUpdateQuery() error = %v, want ErrInvalidClientPatch", err)
	}
}

func TestMapClientRepositoryErrorMapsMissingRowsAndUniqueConflicts(t *testing.T) {
	if !errors.Is(mapClientRepositoryError(pgx.ErrNoRows), outbound.ErrRecordNotFound) {
		t.Fatal("missing row was not mapped to ErrRecordNotFound")
	}
	if !errors.Is(mapClientRepositoryError(uniqueViolation()), outbound.ErrConflict) {
		t.Fatal("unique violation was not mapped to ErrConflict")
	}
}

func stringValue(value string) *string {
	return &value
}

func uniqueViolation() error {
	return &pgconn.PgError{Code: "23505"}
}
