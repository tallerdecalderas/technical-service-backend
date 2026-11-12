package outbound

import (
	"context"
	"time"
)

type EmailSender interface {
	Send(ctx context.Context, recipient, subject, textBody string) error
}

type CatalogImporter interface {
	ImportCatalog(ctx context.Context, spreadsheetID string) (CatalogImportResult, error)
}

type CatalogImportResult struct {
	Imported int
	Skipped  int
}

type ScheduleExporter interface {
	ExportSchedule(ctx context.Context, from, to time.Time) error
}
