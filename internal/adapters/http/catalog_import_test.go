package httpadapter

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"technical-service-backend/internal/application"
	"technical-service-backend/internal/config"
	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type catalogImportUseCasesStub struct {
	csvCalls int
	lastInput application.CatalogCSVImportInput
}

func (s *catalogImportUseCasesStub) ImportCSV(_ context.Context, _ domain.User, input application.CatalogCSVImportInput) (domain.CatalogImport, error) {
	s.csvCalls++
	s.lastInput = input
	return domain.CatalogImport{
		ID:        "import-http-1",
		CompanyID: input.CompanyID,
		Status:    domain.CatalogImportPublished,
		SourceRef: input.FileName,
		RowCount:  1,
		ItemCount: 1,
	}, nil
}

func (s *catalogImportUseCasesStub) Import(_ context.Context, _ domain.User, _ application.CatalogImportInput) (domain.CatalogImport, error) {
	return domain.CatalogImport{}, nil
}

func TestCatalogImportHandlerMultipartCSV(t *testing.T) {
	service := &catalogImportUseCasesStub{}
	auth := clientTestAuth(nil)
	cfg := config.AuthConfig{
		SessionCookieName: "tech-service-session",
		CSRFCookieName:    "tech-service-csrf",
		TrustedOrigins:    []string{"http://localhost:3000"},
	}
	idempotency := &fakeIdempotencyRepository{}
	handler := NewCatalogImportHandler(auth, service, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), idempotency)

	// Build multipart/form-data request
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("companyId", "00000000-0000-0000-0000-000000000001"); err != nil {
		t.Fatalf("write companyId field: %v", err)
	}
	part, err := writer.CreateFormFile("file", "lista-precios.csv")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	csvData := "codigo,nombre,proveedor,costoUSD,margen\nC-01,Caldera Mural,Baxi,1000.00,20\n"
	if _, err := io.WriteString(part, csvData); err != nil {
		t.Fatalf("write csv data: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/catalog/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", "http://localhost:3000")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Idempotency-Key", "import-key-1")
	request.AddCookie(&http.Cookie{Name: "tech-service-session", Value: "session"})
	request.AddCookie(&http.Cookie{Name: "tech-service-csrf", Value: "csrf"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.csvCalls != 1 {
		t.Errorf("expected 1 ImportCSV call, got %d", service.csvCalls)
	}
	if service.lastInput.FileName != "lista-precios.csv" {
		t.Errorf("filename = %s, want lista-precios.csv", service.lastInput.FileName)
	}
	if service.lastInput.CompanyID != "00000000-0000-0000-0000-000000000001" {
		t.Errorf("companyID = %s", service.lastInput.CompanyID)
	}
}

var _ outbound.IdempotencyRepository = (*fakeIdempotencyRepository)(nil)
