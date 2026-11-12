package application

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrCatalogImportForbidden   = errors.New("catalog import forbidden")
	ErrInvalidCatalogImport     = errors.New("invalid catalog import input")
	ErrCatalogImportUnavailable = errors.New("catalog import source unavailable")
	ErrCatalogImportConflict    = outbound.ErrConflict
	ErrCatalogImportRule        = errors.New("catalog import rule violation")
)

type CatalogImportValidationError struct {
	Import domain.CatalogImport
}

func (e *CatalogImportValidationError) Error() string {
	return "catalog import validation failed"
}

type CatalogImportUseCases struct {
	repository outbound.CatalogImportRepository
	reader     outbound.CatalogSheetReader
}

type CatalogImportInput struct {
	CompanyID string
}

type CatalogCSVImportInput struct {
	CompanyID string
	FileName  string
	Reader    io.Reader
}

func NewCatalogImportUseCases(repository outbound.CatalogImportRepository, readers ...outbound.CatalogSheetReader) *CatalogImportUseCases {
	var reader outbound.CatalogSheetReader
	if len(readers) > 0 {
		reader = readers[0]
	}
	return &CatalogImportUseCases{repository: repository, reader: reader}
}

func (s *CatalogImportUseCases) ImportCSV(ctx context.Context, user domain.User, input CatalogCSVImportInput) (domain.CatalogImport, error) {
	actor, err := catalogAdminActor(user)
	if err != nil {
		return domain.CatalogImport{}, err
	}
	if !validUUID(input.CompanyID) || !containsString(actor.CompanyIDs, input.CompanyID) {
		return domain.CatalogImport{}, ErrInvalidCatalogImport
	}
	if input.Reader == nil {
		return domain.CatalogImport{}, ErrInvalidCatalogImport
	}

	sourceRef := strings.TrimSpace(input.FileName)
	if sourceRef == "" {
		sourceRef = "catalog.csv"
	}

	rows, err := readCSVRows(input.Reader)
	if err != nil {
		failure, recordErr := s.repository.RecordFailedCatalogImport(ctx, domain.CatalogImportFailure{
			CompanyID:   input.CompanyID,
			StartedByID: actor.UserID,
			SourceRef:   sourceRef,
			RowCount:    0,
			Errors: []domain.CatalogImportError{
				{Row: 1, Message: fmt.Sprintf("Error al leer el archivo CSV: %s", err.Error())},
			},
		})
		if recordErr != nil {
			return domain.CatalogImport{}, recordErr
		}
		return failure, &CatalogImportValidationError{Import: failure}
	}

	parsed, validationErrors := parseCatalogRows(rows)
	if len(validationErrors) > 0 {
		failure, err := s.repository.RecordFailedCatalogImport(ctx, domain.CatalogImportFailure{
			CompanyID: input.CompanyID, StartedByID: actor.UserID, SourceRef: sourceRef,
			RowCount: len(rows), Errors: validationErrors,
		})
		if err != nil {
			return domain.CatalogImport{}, err
		}
		return failure, &CatalogImportValidationError{Import: failure}
	}

	imported, err := s.repository.PublishCatalog(ctx, domain.CatalogImportPublishInput{
		CompanyID: input.CompanyID, StartedByID: actor.UserID, SourceRef: sourceRef, Rows: parsed,
	})
	if err != nil {
		return domain.CatalogImport{}, err
	}
	return imported, nil
}

func (s *CatalogImportUseCases) Import(ctx context.Context, user domain.User, input CatalogImportInput) (domain.CatalogImport, error) {
	actor, err := catalogAdminActor(user)
	if err != nil {
		return domain.CatalogImport{}, err
	}
	if !validUUID(input.CompanyID) || !containsString(actor.CompanyIDs, input.CompanyID) {
		return domain.CatalogImport{}, ErrInvalidCatalogImport
	}
	if s.reader == nil {
		return domain.CatalogImport{}, ErrCatalogImportUnavailable
	}
	rows, sourceRef, err := s.reader.ReadCatalogRows(ctx)
	if err != nil {
		return domain.CatalogImport{}, ErrCatalogImportUnavailable
	}
	parsed, validationErrors := parseCatalogRows(rows)
	if len(validationErrors) > 0 {
		failure, err := s.repository.RecordFailedCatalogImport(ctx, domain.CatalogImportFailure{
			CompanyID: input.CompanyID, StartedByID: actor.UserID, SourceRef: sourceRef,
			RowCount: len(rows), Errors: validationErrors,
		})
		if err != nil {
			return domain.CatalogImport{}, err
		}
		return failure, &CatalogImportValidationError{Import: failure}
	}
	imported, err := s.repository.PublishCatalog(ctx, domain.CatalogImportPublishInput{
		CompanyID: input.CompanyID, StartedByID: actor.UserID, SourceRef: sourceRef, Rows: parsed,
	})
	if err != nil {
		return domain.CatalogImport{}, err
	}
	return imported, nil
}

func readCSVRows(r io.Reader) ([][]string, error) {
	if r == nil {
		return nil, errors.New("nil csv reader")
	}
	br := bufio.NewReader(r)

	// Descartar BOM UTF-8 si está presente
	bom, err := br.Peek(3)
	if err == nil && len(bom) == 3 && bom[0] == 0xef && bom[1] == 0xbb && bom[2] == 0xbf {
		_, _ = br.Discard(3)
	}

	// Detectar delimitador (, o ;) según la primera línea
	delimiter := ','
	peekBytes, err := br.Peek(2048)
	if err == nil || errors.Is(err, io.EOF) {
		firstLine := string(peekBytes)
		if idx := strings.IndexAny(firstLine, "\r\n"); idx != -1 {
			firstLine = firstLine[:idx]
		}
		if strings.Count(firstLine, ";") > strings.Count(firstLine, ",") {
			delimiter = ';'
		}
	}

	reader := csv.NewReader(br)
	reader.Comma = delimiter
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	return records, nil
}

func parseCatalogRows(rows [][]string) ([]domain.CatalogImportRow, []domain.CatalogImportError) {
	if len(rows) < 2 {
		return nil, []domain.CatalogImportError{{Row: 1, Message: "El archivo no contiene filas de datos."}}
	}
	header := map[string]int{}
	for index, cell := range rows[0] {
		key := normalizeCatalogHeader(cell)
		if key != "" {
			if _, exists := header[key]; !exists {
				header[key] = index
			}
		}
	}
	column := func(names ...string) (int, bool) {
		for _, name := range names {
			if index, ok := header[name]; ok {
				return index, true
			}
		}
		return 0, false
	}
	codeColumn, okCode := column("codigo", "code", "cod")
	nameColumn, okName := column("nombre", "name", "descripcion", "description")
	providerColumn, okProvider := column("proveedor", "provider")
	costColumn, okCost := column("costousd", "costo", "cost")
	marginColumn, okMargin := column("margen", "margenporcentaje", "margin")
	unitColumn, hasUnit := column("unidad", "unit", "medida")

	missing := make([]string, 0, 5)
	if !okCode {
		missing = append(missing, "codigo")
	}
	if !okName {
		missing = append(missing, "nombre")
	}
	if !okProvider {
		missing = append(missing, "proveedor")
	}
	if !okCost {
		missing = append(missing, "costoUSD")
	}
	if !okMargin {
		missing = append(missing, "margen")
	}
	if len(missing) > 0 {
		return nil, []domain.CatalogImportError{{
			Row: 1, Message: "Faltan columnas obligatorias: " + strings.Join(missing, ", ") + ".",
		}}
	}

	result := make([]domain.CatalogImportRow, 0, len(rows)-1)
	errorsFound := make([]domain.CatalogImportError, 0)
	seen := map[string]int{}
	cellAt := func(row []string, index int) string {
		if index < 0 || index >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[index])
	}
	for rowIndex := 1; rowIndex < len(rows); rowIndex++ {
		row := rows[rowIndex]
		code := cellAt(row, codeColumn)
		name := cellAt(row, nameColumn)
		provider := cellAt(row, providerColumn)
		cost := cellAt(row, costColumn)
		margin := cellAt(row, marginColumn)
		if code == "" && name == "" && provider == "" && cost == "" && margin == "" {
			continue
		}
		line := rowIndex + 1
		if code == "" || utf8.RuneCountInString(code) > 100 {
			errorsFound = append(errorsFound, domain.CatalogImportError{Row: line, Code: code, Message: "Código vacío o mayor a 100 caracteres."})
			continue
		}
		if first, duplicate := seen[strings.ToUpper(code)]; duplicate {
			errorsFound = append(errorsFound, domain.CatalogImportError{
				Row: line, Code: code, Message: "Código duplicado; ya aparece en la fila " + strconv.Itoa(first) + ".",
			})
			continue
		}
		seen[strings.ToUpper(code)] = line
		if name == "" || utf8.RuneCountInString(name) > 300 {
			errorsFound = append(errorsFound, domain.CatalogImportError{Row: line, Code: code, Message: "Nombre vacío o mayor a 300 caracteres."})
			continue
		}
		if provider == "" || utf8.RuneCountInString(provider) > maxCatalogProviderNameRunes {
			errorsFound = append(errorsFound, domain.CatalogImportError{Row: line, Code: code, Message: "Proveedor vacío o mayor a 200 caracteres."})
			continue
		}
		if !validNonNegativeDecimal(cost) {
			errorsFound = append(errorsFound, domain.CatalogImportError{Row: line, Code: code, Message: "Costo USD inválido; usá un decimal no negativo de hasta seis decimales."})
			continue
		}
		if !validNonNegativeDecimal(margin) {
			errorsFound = append(errorsFound, domain.CatalogImportError{Row: line, Code: code, Message: "Margen inválido; usá un porcentaje no negativo de hasta seis decimales."})
			continue
		}
		marginValue, err := parseDecimal(margin)
		limit, _ := parseDecimal(maxCatalogMarginPercent)
		if err != nil || marginValue.Cmp(limit) > 0 {
			errorsFound = append(errorsFound, domain.CatalogImportError{Row: line, Code: code, Message: "Margen fuera de rango; el máximo es 1000."})
			continue
		}
		var unit *string
		if hasUnit {
			if value := cellAt(row, unitColumn); value != "" {
				unit = &value
			}
		}
		result = append(result, domain.CatalogImportRow{
			Code: code, Name: name, Unit: unit, ProviderName: provider,
			CostUsd: cost, MarginPercent: margin,
		})
	}
	if len(result) == 0 && len(errorsFound) == 0 {
		errorsFound = append(errorsFound, domain.CatalogImportError{Row: 1, Message: "El archivo no contiene filas de datos."})
	}
	return result, errorsFound
}

func normalizeCatalogHeader(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, char := range value {
		switch {
		case unicode.IsLetter(char) || unicode.IsDigit(char):
			builder.WriteRune(stripCatalogAccent(char))
		}
	}
	return builder.String()
}

func stripCatalogAccent(char rune) rune {
	switch char {
	case 'á', 'à', 'ä', 'â':
		return 'a'
	case 'é', 'è', 'ë', 'ê':
		return 'e'
	case 'í', 'ì', 'ï', 'î':
		return 'i'
	case 'ó', 'ò', 'ö', 'ô':
		return 'o'
	case 'ú', 'ù', 'ü', 'û':
		return 'u'
	case 'ñ':
		return 'n'
	default:
		return char
	}
}
