package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/application"
	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type userDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type UserRepository struct {
	db           userDB
	queryTimeout time.Duration
}

func NewUserRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *UserRepository {
	return &UserRepository{db: pool, queryTimeout: queryTimeout}
}

func (r *UserRepository) CreateUser(ctx context.Context, input domain.UserCreateInput) (string, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	passwordHash, err := application.BcryptGenerate(input.Password)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}

	var id string
	if err := r.db.QueryRow(queryCtx, `
insert into public."user" (name, email, role, "passwordHash", "isActive", phone, dni, company_id)
values ($1, $2, $3::text, $4, true, $5, $6, $7)
returning id::text
`, input.Name, input.Email, input.Role, passwordHash, input.Phone, input.DNI, input.CompanyID).Scan(&id); err != nil {
		return "", fmt.Errorf("create user: %w", err)
	}
	return id, nil
}

func (r *UserRepository) GetUser(ctx context.Context, id string) (*domain.User, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	var user domain.User
	var passwordHash, avatar pgtype.Text
	var createdAt, updatedAt pgtype.Timestamptz

	if err := r.db.QueryRow(queryCtx, `
select id::text, name, email, role::text, passwordHash, coalesce(phone, ''), coalesce(dni, ''),
       coalesce(avatar, ''), coalesce("isActive", false), coalesce(u."createdAt", now()), coalesce(u."updatedAt", now()
from public."user" as u
where u.id::text = $1
`, id).Scan(
		&user.ID, &user.Name, &user.Email, &user.Role, &passwordHash,
		&user.Phone, &user.DNI, &avatar, &user.Active, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, outbound.ErrRecordNotFound
		}
		return nil, fmt.Errorf("get user: %w", err)
	}

	if avatar.Valid {
		user.Avatar = avatar.String
	}
	if createdAt.Valid {
		user.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		user.UpdatedAt = updatedAt.Time
	}
	return &user, nil
}

func (r *UserRepository) ListUsers(ctx context.Context, user domain.User) ([]domain.UserSummary, error) {
	if !user.Active || (!strings.EqualFold(user.Role, "ADMIN") && !strings.EqualFold(user.Role, "TECHNICIAN")) {
		return nil, errors.New("company operation forbidden")
	}

	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	rows, err := r.db.Query(queryCtx, `
select u.id::text, u.name, u.email, u.role::text, coalesce(u."isActive", false), coalesce(cl.name, '')
from public."user" as u
left join public.company as cl on cl.id = u.company_id
where ($1::text = 'ADMIN' or exists (
    select 1 from public.user_company_memberships as m
    where m.user_id = u.id
      and m.is_active
      and m.company_id::text = any($2::text[])
))
order by lower(u.name), u.id
`, user.Role, append([]string(nil), user.CompanyIDs...))
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]domain.UserSummary, 0)
	for rows.Next() {
		var u domain.UserSummary
		var companyName sql.NullString
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.IsActive, &companyName); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		if companyName.Valid {
			u.CompanyName = companyName.String
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read users: %w", err)
	}
	return users, nil
}

func (r *UserRepository) UpdateUser(ctx context.Context, id string, input domain.UserUpdateInput) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	current, err := r.GetUser(ctx, id)
	if err != nil {
		return err
	}

	name := current.Name
	email := current.Email
	role := current.Role
	phone := current.Phone
	dni := current.DNI
	isActive := current.Active
	companyID := current.ID

	if input.Name != nil {
		if strings.TrimSpace(*input.Name) == "" {
			return errors.New("name is required")
		}
		name = strings.TrimSpace(*input.Name)
	}
	if input.Email != nil {
		if strings.TrimSpace(*input.Email) == "" {
			return errors.New("email is required")
		}
		email = strings.TrimSpace(*input.Email)
	}
	if input.Role != nil {
		role = strings.TrimSpace(*input.Role)
	}
	if input.Phone != nil {
		phone = strings.TrimSpace(*input.Phone)
	}
	if input.DNI != nil {
		dni = strings.TrimSpace(*input.DNI)
	}
	if input.IsActive != nil {
		isActive = *input.IsActive
	}
	if input.CompanyID != nil {
		companyID = *input.CompanyID
	}

	result, err := r.db.Exec(queryCtx, `
update public."user"
set name = $2, email = $3, role::text = $4, phone = $5, dni = $6, "isActive" = $7, company_id = $8, "updatedAt" = now()
where id::text = $1
`, id, name, email, role, phone, dni, isActive, companyID)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	if result.RowsAffected() == 0 {
		return errors.New("user not found")
	}
	return nil
}

func (r *UserRepository) DeactivateUser(ctx context.Context, id string) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	result, err := r.db.Exec(queryCtx, `
update public."user"
set "isActive" = false, "updatedAt" = now()
where id::text = $1
`, id)
	if err != nil {
		return fmt.Errorf("deactivate user: %w", err)
	}
	if result.RowsAffected() == 0 {
		return errors.New("user not found")
	}
	return nil
}

func (r *UserRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.UserRepository = (*UserRepository)(nil)