package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type TechnicianRepository struct {
	pool         *pgxpool.Pool
	queryTimeout time.Duration
}

func NewTechnicianRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *TechnicianRepository {
	return &TechnicianRepository{pool: pool, queryTimeout: queryTimeout}
}

func (r *TechnicianRepository) ListTechnicians(ctx context.Context, filter domain.TechnicianFilter) ([]domain.Technician, int, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	const predicate = `u.role::text = 'TECHNICIAN'
  and (
      ($1::boolean and u.id::text = $2)
      or (
          not $1::boolean
          and exists (
              select 1
              from public.user_company_memberships as m
              where m.user_id = u.id
                and m.is_active
                and m.company_id::text = any($3::text[])
          )
      )
  )`
	var total int
	if err := r.pool.QueryRow(queryCtx, `select count(*) from public."user" as u where `+predicate,
		filter.SelfOnly, filter.UserID, filter.CompanyIDs).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count technicians: %w", err)
	}
	rows, err := r.pool.Query(queryCtx, `
select u.id::text, u.name, u.email, u.phone, u.dni, u.role::text,
       coalesce(u."isActive", false), u.avatar, u."createdAt", u."updatedAt"
from public."user" as u
where `+predicate+`
order by lower(u.name), u.id
limit $4 offset $5
`, filter.SelfOnly, filter.UserID, filter.CompanyIDs, filter.PageSize, (filter.Page-1)*filter.PageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("query technicians: %w", err)
	}
	defer rows.Close()
	technicians := make([]domain.Technician, 0)
	for rows.Next() {
		technician, err := scanTechnician(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan technician: %w", err)
		}
		technicians = append(technicians, technician)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read technicians: %w", err)
	}
	return technicians, total, nil
}

func (r *TechnicianRepository) GetTechnician(ctx context.Context, id string, filter domain.TechnicianFilter) (domain.Technician, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	technician, err := scanTechnician(r.pool.QueryRow(queryCtx, `
select u.id::text, u.name, u.email, u.phone, u.dni, u.role::text,
       coalesce(u."isActive", false), u.avatar, u."createdAt", u."updatedAt"
from public."user" as u
where u.id = $1::uuid
  and u.role::text = 'TECHNICIAN'
  and (
      ($2::boolean and u.id::text = $3)
      or (
          not $2::boolean
          and exists (
              select 1
              from public.user_company_memberships as m
              where m.user_id = u.id
                and m.is_active
                and m.company_id::text = any($4::text[])
          )
      )
  )
`, id, filter.SelfOnly, filter.UserID, filter.CompanyIDs))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Technician{}, outbound.ErrRecordNotFound
		}
		return domain.Technician{}, fmt.Errorf("query technician: %w", err)
	}
	return technician, nil
}

func (r *TechnicianRepository) CreateTechnician(ctx context.Context, draft domain.TechnicianDraft) (domain.Technician, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.pool.Begin(queryCtx)
	if err != nil {
		return domain.Technician{}, fmt.Errorf("begin technician creation: %w", err)
	}
	defer rollback(tx)

	if err := verifyActiveCompanies(queryCtx, tx, draft.CompanyIDs); err != nil {
		return domain.Technician{}, err
	}
	var id string
	if err := tx.QueryRow(queryCtx, `
insert into public."user" (name, email, role, "passwordHash", phone, dni, "isActive")
values ($1, $2, 'TECHNICIAN', $3, $4, $5, true)
returning id::text
`, draft.Name, draft.Email, draft.PasswordHash, draft.Phone, draft.DNI).Scan(&id); err != nil {
		return domain.Technician{}, mapTechnicianRepositoryError(err)
	}
	if _, err := tx.Exec(queryCtx, `
insert into public.user_identities (user_id, provider, provider_subject, active)
values ($1::uuid, 'supabase', $2, true)
`, id, draft.IdentityID); err != nil {
		return domain.Technician{}, mapTechnicianRepositoryError(err)
	}
	for _, companyID := range draft.CompanyIDs {
		if _, err := tx.Exec(queryCtx, `
insert into public.user_company_memberships (user_id, company_id, is_active)
values ($1::uuid, $2::uuid, true)
on conflict (user_id, company_id) do update set is_active = true
`, id, companyID); err != nil {
			return domain.Technician{}, fmt.Errorf("create technician company membership: %w", err)
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Technician{}, fmt.Errorf("commit technician creation: %w", err)
	}
	return r.GetTechnician(ctx, id, domain.TechnicianFilter{CompanyIDs: draft.CompanyIDs})
}

func (r *TechnicianRepository) UpdateTechnician(ctx context.Context, id string, patch domain.TechnicianPatch, companyIDs []string) (domain.Technician, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.pool.Begin(queryCtx)
	if err != nil {
		return domain.Technician{}, fmt.Errorf("begin technician update: %w", err)
	}
	defer rollback(tx)

	var active bool
	if err := tx.QueryRow(queryCtx, `
select coalesce(u."isActive", false)
from public."user" as u
where u.id = $1::uuid and u.role::text = 'TECHNICIAN'
  and exists (
      select 1
      from public.user_company_memberships as m
      where m.user_id = u.id and m.is_active
        and m.company_id::text = any($2::text[])
  )
for update
`, id, companyIDs).Scan(&active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Technician{}, outbound.ErrRecordNotFound
		}
		return domain.Technician{}, fmt.Errorf("load technician for update: %w", err)
	}

	query, args := buildTechnicianUpdate(id, patch, companyIDs)
	if query != "" {
		if _, err := tx.Exec(queryCtx, query, args...); err != nil {
			return domain.Technician{}, mapTechnicianRepositoryError(err)
		}
	}
	if patch.CompanyIDs.Set {
		if err := verifyActiveCompanies(queryCtx, tx, *patch.CompanyIDs.Value); err != nil {
			return domain.Technician{}, err
		}
		if _, err := tx.Exec(queryCtx, `
delete from public.user_company_memberships
where user_id = $1::uuid and company_id::text = any($2::text[])
`, id, companyIDs); err != nil {
			return domain.Technician{}, fmt.Errorf("replace technician company memberships: %w", err)
		}
		for _, companyID := range *patch.CompanyIDs.Value {
			if _, err := tx.Exec(queryCtx, `
insert into public.user_company_memberships (user_id, company_id, is_active)
values ($1::uuid, $2::uuid, true)
on conflict (user_id, company_id) do update set is_active = true
`, id, companyID); err != nil {
				return domain.Technician{}, fmt.Errorf("assign technician company membership: %w", err)
			}
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Technician{}, fmt.Errorf("commit technician update: %w", err)
	}
	return r.GetTechnician(ctx, id, domain.TechnicianFilter{CompanyIDs: companyIDs})
}

func buildTechnicianUpdate(id string, patch domain.TechnicianPatch, companyIDs []string) (string, []any) {
	sets := make([]string, 0, 5)
	args := make([]any, 0, 7)
	appendValue := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%q = $%d", column, len(args)))
	}
	if patch.Name.Set {
		appendValue("name", patch.Name.Value)
	}
	if patch.Phone.Set {
		appendValue("phone", nullableString(patch.Phone))
	}
	if patch.DNI.Set {
		appendValue("dni", nullableString(patch.DNI))
	}
	if patch.Active.Set {
		appendValue("isActive", patch.Active.Value)
	}
	if patch.Avatar.Set {
		appendValue("avatar", nullableString(patch.Avatar))
	}
	if len(sets) == 0 {
		return "", nil
	}
	sets = append(sets, `"updatedAt" = now()`)
	args = append(args, id, companyIDs)
	idArg := len(args) - 1
	companyArg := len(args)
	return fmt.Sprintf(`
update public."user" as u
set %s
where u.id = $%d::uuid
  and u.role::text = 'TECHNICIAN'
  and exists (
      select 1
      from public.user_company_memberships as m
      where m.user_id = u.id and m.is_active
        and m.company_id::text = any($%d::text[])
  )
`, strings.Join(sets, ", "), idArg, companyArg), args
}

func verifyActiveCompanies(ctx context.Context, tx pgx.Tx, companyIDs []string) error {
	var count int
	if err := tx.QueryRow(ctx, `
select count(*) from public.company
where id::text = any($1::text[]) and "isActive"
`, companyIDs).Scan(&count); err != nil {
		return fmt.Errorf("validate technician companies: %w", err)
	}
	if count != len(companyIDs) {
		return outbound.ErrRecordNotFound
	}
	return nil
}

func scanTechnician(row pgx.Row) (domain.Technician, error) {
	var technician domain.Technician
	var phone, dni, avatar pgtype.Text
	if err := row.Scan(
		&technician.ID, &technician.Name, &technician.Email, &phone, &dni,
		&technician.Role, &technician.Active, &avatar, &technician.CreatedAt, &technician.UpdatedAt,
	); err != nil {
		return domain.Technician{}, err
	}
	technician.Phone = textPointer(phone)
	technician.DNI = textPointer(dni)
	technician.Avatar = textPointer(avatar)
	return technician, nil
}

func nullableString(field domain.Field[string]) *string {
	if !field.Set || field.Value == nil {
		return nil
	}
	return field.Value
}

func mapTechnicianRepositoryError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return outbound.ErrRecordNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return outbound.ErrConflict
		case "23503":
			return outbound.ErrRecordNotFound
		}
	}
	return fmt.Errorf("technician repository operation: %w", err)
}

func (r *TechnicianRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.TechnicianRepository = (*TechnicianRepository)(nil)
