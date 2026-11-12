package postgres

import (
	"strings"
	"testing"

	"technical-service-backend/internal/ports/outbound"
)

func TestAuthRepositoryQueriesUseExplicitIdentityAndSessionBoundaries(t *testing.T) {
	for _, fragment := range []string{
		`public.user_identities`,
		`public."user"`,
		`public.user_company_memberships`,
		`public.company`,
		`ui.provider = $1`,
		`ui.provider_subject = $2`,
		`ui.active`,
		`c."isActive"`,
	} {
		if !strings.Contains(resolveUserSQL, fragment) {
			t.Errorf("resolveUserSQL does not contain %q", fragment)
		}
	}
	for _, fragment := range []string{
		`public.app_sessions`,
		`s.token_hash = $1`,
		`public.user_company_memberships`,
		`public.company`,
	} {
		if !strings.Contains(findSessionSQL, fragment) {
			t.Errorf("findSessionSQL does not contain %q", fragment)
		}
	}
}

func TestAuthRepositorySatisfiesPort(t *testing.T) {
	var _ outbound.AuthRepository = (*AuthRepository)(nil)
}
