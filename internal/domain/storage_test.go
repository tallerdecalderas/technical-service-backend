package domain_test

import (
	"testing"

	"technical-service-backend/internal/domain"
)

func TestStorageValidation(t *testing.T) {
	t.Run("IsValidCategory", func(t *testing.T) {
		valid := []string{"photos", "receipts", "documents", " PHOTOS ", "Receipts"}
		for _, cat := range valid {
			if !domain.IsValidCategory(cat) {
				t.Errorf("expected category %q to be valid", cat)
			}
		}

		invalid := []string{"videos", "binaries", "", "unknown"}
		for _, cat := range invalid {
			if domain.IsValidCategory(cat) {
				t.Errorf("expected category %q to be invalid", cat)
			}
		}
	})

	t.Run("IsAllowedMIME", func(t *testing.T) {
		valid := []string{"image/jpeg", "image/png", "image/webp", "application/pdf", " IMAGE/JPEG "}
		for _, mime := range valid {
			if !domain.IsAllowedMIME(mime) {
				t.Errorf("expected MIME %q to be allowed", mime)
			}
		}

		invalid := []string{"image/svg+xml", "text/html", "application/javascript", "video/mp4", ""}
		for _, mime := range invalid {
			if domain.IsAllowedMIME(mime) {
				t.Errorf("expected MIME %q to be disallowed", mime)
			}
		}
	})
}
