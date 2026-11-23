package postgres

import (
	"context"
	"database/sql"
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

type templateDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type TemplateRepository struct {
	db           templateDB
	queryTimeout time.Duration
}

func NewTemplateRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *TemplateRepository {
	return &TemplateRepository{db: pool, queryTimeout: queryTimeout}
}

func (r *TemplateRepository) CreateTemplate(ctx context.Context, input domain.TemplateDraft) (string, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	var id string
	if err := r.db.QueryRow(queryCtx, `
insert into public."template" (name, "content", "isActive", company_id)
values ($1, $2, true, $3)
returning id::text
`, input.Name, input.Content, input.CompanyID).Scan(&id); err != nil {
		return "", fmt.Errorf("create template: %w", err)
	}
	return id, nil
}

func (r *TemplateRepository) GetTemplate(ctx context.Context, id string) (*domain.Template, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	var template domain.Template
	var content sql.NullString
	var createdAt, updatedAt pgtype.Timestamptz

	if err := r.db.QueryRow(queryCtx, `
select id::text, name, "content", "isActive", company_id,
       coalesce("createdAt", now()), coalesce("updatedAt", now())
from public."template" as t
where t.id::text = $1
`, id).Scan(
		&template.ID, &template.CompanyID, &content, &template.IsActive,
		&template.CompanyID, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, outbound.ErrRecordNotFound
		}
		return nil, fmt.Errorf("get template: %w", err)
	}

	if content.Valid {
		template.Content = content.String
	}
	if createdAt.Valid {
		template.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		template.UpdatedAt = updatedAt.Time
	}
	return &template, nil
}

func (r *TemplateRepository) ListTemplates(ctx context.Context, companyID string, filter domain.TemplateFilter) (domain.TemplatePage, error) {
	if filter.Page == 0 {
		filter.Page = 1
	}
	if filter.PageSize == 0 {
		filter.PageSize = 25
	}

	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	rows, err := r.db.Query(queryCtx, `
select t.id::text, t.name, t."content", t."isActive",
       coalesce(cl.name, '')
from public."template" as t
left join public.company as cl on cl.id = t.company_id
where t.company_id::text = $1
order by lower(t.name), t.id
`, companyID)
	if err != nil {
		return domain.TemplatePage{}, fmt.Errorf("list templates: %w", err)
	}
	defer rows.Close()

	templates := make([]domain.Template, 0)
	for rows.Next() {
		var t domain.Template
		var companyName sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &t.Content, &t.IsActive, &companyName); err != nil {
			return domain.TemplatePage{}, fmt.Errorf("scan template: %w", err)
		}
		if companyName.Valid {
			t.CompanyName = companyName.String
		}
		templates = append(templates, t)
	}
	if err := rows.Err(); err != nil {
		return domain.TemplatePage{}, fmt.Errorf("read templates: %w", err)
	}

	total := len(templates)
	start := (filter.Page - 1) * filter.PageSize
	end := start + filter.PageSize
	if start < len(templates) {
		if end > len(templates) {
			end = len(templates)
		}
		templates = templates[start:end]
	} else {
		templates = []domain.Template{}
	}

	return domain.TemplatePage{
		Data:      templates,
		Page:      filter.Page,
		PageSize:  filter.PageSize,
		Total:     total,
	}, nil
}

func (r *TemplateRepository) UpdateTemplate(ctx context.Context, id string, input domain.TemplatePatch) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	_, err := r.db.Exec(queryCtx, `
update public."template"
set name = coalesce($2, name),
    "content" = coalesce($3, "content"),
    "isActive" = coalesce($4, "isActive"),
    "updatedAt" = now()
where id::text = $1
`, id, input.Name, input.Content, input.IsActive)
	if err != nil {
		return fmt.Errorf("update template: %w", err)
	}
	return nil
}

func (r *TemplateRepository) DeleteTemplate(ctx context.Context, id string) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	result, err := r.db.Exec(queryCtx, `
delete from public."template"
where id::text = $1
`, id)
	if err != nil {
		return fmt.Errorf("delete template: %w", err)
	}
	if result.RowsAffected() == 0 {
		return errors.New("template not found")
	}
	return nil
}

func (r *TemplateRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.TemplateRepository = (*TemplateRepository)(nil)
