package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

const resolveUserSQL = `
select
    u.id::text,
    u.email,
    u.name,
    u.role::text,
    coalesce(u.avatar, ''),
    coalesce(u."isActive", false),
    coalesce(m.company_id::text, ''),
    coalesce(m.is_active and c."isActive", false)
from public.user_identities as ui
join public."user" as u on u.id = ui.user_id
left join public.user_company_memberships as m on m.user_id = u.id
left join public.company as c on c.id = m.company_id
where ui.provider = $1
  and ui.provider_subject = $2
  and ui.active
order by m.company_id
`

const findSessionSQL = `
select
    s.token_hash,
    s.csrf_token_hash,
    s.user_id::text,
    s.created_at,
    s.expires_at,
    s.revoked_at,
    s.last_seen_at,
    u.email,
    u.name,
    u.role::text,
    coalesce(u.avatar, ''),
    coalesce(u."isActive", false),
    coalesce(m.company_id::text, ''),
    coalesce(m.is_active and c."isActive", false)
from public.app_sessions as s
join public."user" as u on u.id = s.user_id
left join public.user_company_memberships as m on m.user_id = u.id
left join public.company as c on c.id = m.company_id
where s.token_hash = $1
order by m.company_id
`

type authDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type AuthRepository struct {
	db           authDB
	queryTimeout time.Duration
}

func NewAuthRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *AuthRepository {
	return &AuthRepository{db: pool, queryTimeout: queryTimeout}
}

func (r *AuthRepository) ResolveUser(ctx context.Context, provider, subject string) (domain.User, []domain.CompanyMembership, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	rows, err := r.db.Query(queryCtx, resolveUserSQL, provider, subject)
	if err != nil {
		return domain.User{}, nil, fmt.Errorf("query linked user: %w", err)
	}
	defer rows.Close()
	var user domain.User
	memberships := make([]domain.CompanyMembership, 0)
	found := false
	for rows.Next() {
		var companyID string
		var membershipActive bool
		var active bool
		var candidate domain.User
		if err := rows.Scan(
			&candidate.ID, &candidate.Email, &candidate.Name, &candidate.Role,
			&candidate.Avatar, &active, &companyID, &membershipActive,
		); err != nil {
			return domain.User{}, nil, fmt.Errorf("scan linked user: %w", err)
		}
		candidate.Active = active
		if !found {
			user = candidate
			found = true
		}
		if companyID != "" {
			memberships = append(memberships, domain.CompanyMembership{
				CompanyID: companyID,
				Active:    membershipActive,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return domain.User{}, nil, fmt.Errorf("read linked user: %w", err)
	}
	if !found {
		return domain.User{}, nil, outbound.ErrRecordNotFound
	}
	return user, memberships, nil
}

func (r *AuthRepository) CreateSession(ctx context.Context, session domain.AuthSession) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	_, err := r.db.Exec(queryCtx, `
insert into public.app_sessions (token_hash, csrf_token_hash, user_id, created_at, expires_at)
values ($1, $2, $3, $4, $5)
`, session.TokenHash, session.CSRFTokenHash, session.UserID, session.CreatedAt, session.ExpiresAt)
	if err != nil {
		return fmt.Errorf("insert application session: %w", err)
	}
	return nil
}

func (r *AuthRepository) FindSession(ctx context.Context, tokenHash string) (domain.AuthSession, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	rows, err := r.db.Query(queryCtx, findSessionSQL, tokenHash)
	if err != nil {
		return domain.AuthSession{}, fmt.Errorf("query application session: %w", err)
	}
	defer rows.Close()
	var record domain.AuthSession
	memberships := make([]domain.CompanyMembership, 0)
	found := false
	for rows.Next() {
		var userActive, membershipActive bool
		var companyID string
		var revokedAt, lastSeenAt pgtype.Timestamptz
		var candidate domain.AuthSession
		if err := rows.Scan(
			&candidate.TokenHash, &candidate.CSRFTokenHash, &candidate.UserID,
			&candidate.CreatedAt, &candidate.ExpiresAt, &revokedAt, &lastSeenAt,
			&candidate.User.Email, &candidate.User.Name, &candidate.User.Role,
			&candidate.User.Avatar, &userActive, &companyID, &membershipActive,
		); err != nil {
			return domain.AuthSession{}, fmt.Errorf("scan application session: %w", err)
		}
		candidate.User.ID = candidate.UserID
		candidate.User.Active = userActive
		if revokedAt.Valid {
			value := revokedAt.Time
			candidate.RevokedAt = &value
		}
		if lastSeenAt.Valid {
			value := lastSeenAt.Time
			candidate.LastSeenAt = &value
		}
		if !found {
			record = candidate
			found = true
		}
		if companyID != "" {
			memberships = append(memberships, domain.CompanyMembership{
				CompanyID: companyID,
				Active:    membershipActive,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return domain.AuthSession{}, fmt.Errorf("read application session: %w", err)
	}
	if !found {
		return domain.AuthSession{}, outbound.ErrRecordNotFound
	}
	record.Memberships = memberships
	return record, nil
}

func (r *AuthRepository) RevokeSession(ctx context.Context, tokenHash string, at time.Time) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	result, err := r.db.Exec(queryCtx, `
update public.app_sessions
set revoked_at = coalesce(revoked_at, $2)
where token_hash = $1
`, tokenHash, at)
	if err != nil {
		return fmt.Errorf("revoke application session: %w", err)
	}
	if result.RowsAffected() == 0 {
		return nil
	}
	return nil
}

func (r *AuthRepository) UpdateLastSeen(ctx context.Context, tokenHash string, at time.Time) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	result, err := r.db.Exec(queryCtx, `
update public.app_sessions
set last_seen_at = $2
where token_hash = $1 and revoked_at is null
`, tokenHash, at)
	if err != nil {
		return fmt.Errorf("update application session activity: %w", err)
	}
	if result.RowsAffected() == 0 {
		return outbound.ErrRecordNotFound
	}
	return nil
}

func (r *AuthRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

func isMissingAuthRecord(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, outbound.ErrRecordNotFound)
}

var _ outbound.AuthRepository = (*AuthRepository)(nil)
