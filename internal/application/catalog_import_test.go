package application

import (
	"context"
	"strings"
	"testing"

	"technical-service-backend/internal/domain"
)

type catalogImportRepoStub struct {
	publishInput domain.CatalogImportPublishInput
	failureInput domain.CatalogImportFailure
	publishCalls int
	failureCalls int
}

func (s *catalogImportRepoStub) PublishCatalog(_ context.Context, input domain.CatalogImportPublishInput) (domain.CatalogImport, error) {
	s.publishCalls++
	s.publishInput = input
	return domain.CatalogImport{
		ID:          "import-1",
		CompanyID:   input.CompanyID,
		StartedByID: input.StartedByID,
		Status:      domain.CatalogImportPublished,
		SourceRef:   input.SourceRef,
		RowCount:    len(input.Rows),
		ItemCount:   len(input.Rows),
	}, nil
}

func (s *catalogImportRepoStub) RecordFailedCatalogImport(_ context.Context, failure domain.CatalogImportFailure) (domain.CatalogImport, error) {
	s.failureCalls++
	s.failureInput = failure
	return domain.CatalogImport{
		ID:          "import-fail-1",
		CompanyID:   failure.CompanyID,
		StartedByID: failure.StartedByID,
		Status:      domain.CatalogImportFailed,
		SourceRef:   failure.SourceRef,
		RowCount:    failure.RowCount,
		ErrorCount:  len(failure.Errors),
		Errors:      failure.Errors,
	}, nil
}

func TestCatalogImportCSVSuccess(t *testing.T) {
	repo := &catalogImportRepoStub{}
	useCases := NewCatalogImportUseCases(repo)

	user := domain.User{
		ID:         "admin-user-id",
		Role:       "ADMIN",
		Active:     true,
		CompanyIDs: []string{"00000000-0000-0000-0000-000000000001"},
	}

	csvContent := "codigo,nombre,proveedor,costoUSD,margen,unidad\n" +
		"VALV-01,Válvula de seguridad,Proveedor Central,25.50,30,u\n" +
		"TERM-02,Termocupla 600mm,TermoParts,12.00,45.5,u\n"

	result, err := useCases.ImportCSV(context.Background(), user, CatalogCSVImportInput{
		CompanyID: "00000000-0000-0000-0000-000000000001",
		FileName:  "precios-test.csv",
		Reader:    strings.NewReader(csvContent),
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if result.Status != domain.CatalogImportPublished {
		t.Errorf("status = %s, want PUBLISHED", result.Status)
	}
	if repo.publishCalls != 1 {
		t.Errorf("publish calls = %d, want 1", repo.publishCalls)
	}
	if len(repo.publishInput.Rows) != 2 {
		t.Fatalf("expected 2 parsed rows, got %d", len(repo.publishInput.Rows))
	}
	if repo.publishInput.Rows[0].Code != "VALV-01" {
		t.Errorf("row 0 code = %s, want VALV-01", repo.publishInput.Rows[0].Code)
	}
	if repo.publishInput.SourceRef != "precios-test.csv" {
		t.Errorf("sourceRef = %s, want precios-test.csv", repo.publishInput.SourceRef)
	}
}

func TestCatalogImportCSVSemicolonDelimiterAndBOM(t *testing.T) {
	repo := &catalogImportRepoStub{}
	useCases := NewCatalogImportUseCases(repo)

	user := domain.User{
		ID:         "admin-user-id",
		Role:       "ADMIN",
		Active:     true,
		CompanyIDs: []string{"00000000-0000-0000-0000-000000000001"},
	}

	// UTF-8 BOM + semicolon-separated CSV
	csvContent := "\xef\xbb\xbfcodigo;nombre;proveedor;costoUSD;margen\n" +
		"BOM-01;Bomba circuladora;Baxi Repuestos;150.00;35\n"

	result, err := useCases.ImportCSV(context.Background(), user, CatalogCSVImportInput{
		CompanyID: "00000000-0000-0000-0000-000000000001",
		FileName:  "baxi.csv",
		Reader:    strings.NewReader(csvContent),
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if result.Status != domain.CatalogImportPublished {
		t.Errorf("status = %s, want PUBLISHED", result.Status)
	}
	if len(repo.publishInput.Rows) != 1 {
		t.Fatalf("expected 1 parsed row, got %d", len(repo.publishInput.Rows))
	}
	if repo.publishInput.Rows[0].Code != "BOM-01" {
		t.Errorf("code = %s, want BOM-01", repo.publishInput.Rows[0].Code)
	}
}

func TestCatalogImportCSVValidationDuplicateCode(t *testing.T) {
	repo := &catalogImportRepoStub{}
	useCases := NewCatalogImportUseCases(repo)

	user := domain.User{
		ID:         "admin-user-id",
		Role:       "ADMIN",
		Active:     true,
		CompanyIDs: []string{"00000000-0000-0000-0000-000000000001"},
	}

	csvContent := "codigo,nombre,proveedor,costoUSD,margen\n" +
		"DUP-01,Repuesto A,Proveedor 1,10.00,20\n" +
		"dup-01,Repuesto B,Proveedor 2,15.00,25\n"

	_, err := useCases.ImportCSV(context.Background(), user, CatalogCSVImportInput{
		CompanyID: "00000000-0000-0000-0000-000000000001",
		FileName:  "dups.csv",
		Reader:    strings.NewReader(csvContent),
	})
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}

	if repo.failureCalls != 1 {
		t.Errorf("failure calls = %d, want 1", repo.failureCalls)
	}
	if len(repo.failureInput.Errors) == 0 {
		t.Error("expected duplicate error in failure input")
	}
}
