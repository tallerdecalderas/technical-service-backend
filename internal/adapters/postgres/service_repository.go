package postgres

import (
	"context"
	"encoding/json"
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

type ServiceRepository struct {
	pool         *pgxpool.Pool
	queryTimeout time.Duration
}

func NewServiceRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *ServiceRepository {
	return &ServiceRepository{pool: pool, queryTimeout: queryTimeout}
}

func (r *ServiceRepository) ListServices(ctx context.Context, filter domain.ServiceFilter) ([]domain.Service, int, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	where, args := serviceListWhere(filter)
	var total int
	if err := r.pool.QueryRow(queryCtx, `select count(*) from public."service" as s where `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count services: %w", err)
	}
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := r.pool.Query(queryCtx, serviceSelectSQL+`
where `+where+`
order by s."scheduledDate" desc, s."orderNumber" desc, s.id desc
limit $`+fmt.Sprint(len(args)-1)+` offset $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query services: %w", err)
	}
	defer rows.Close()
	services := make([]domain.Service, 0)
	for rows.Next() {
		service, err := scanService(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan service: %w", err)
		}
		services = append(services, service)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read services: %w", err)
	}
	return services, total, nil
}

func (r *ServiceRepository) GetService(ctx context.Context, id string, actor domain.ServiceActor) (domain.Service, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	where := `s.id = $1::uuid and s."companyId"::text = any($2::text[])
and ($3 <> 'TECHNICIAN' or s."technicianId"::text = $4)`
	service, err := scanService(r.pool.QueryRow(queryCtx, serviceSelectSQL+` where `+where,
		id, actor.CompanyIDs, actor.Role, actor.UserID))
	if err != nil {
		return domain.Service{}, mapServiceRepositoryError(err)
	}
	return service, nil
}

func (r *ServiceRepository) CreateService(ctx context.Context, draft domain.ServiceDraft) (domain.Service, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.pool.Begin(queryCtx)
	if err != nil {
		return domain.Service{}, fmt.Errorf("begin service creation: %w", err)
	}
	defer rollback(tx)

	var companyActive, categoryActive, clientExists bool
	if err := tx.QueryRow(queryCtx, `
select coalesce("isActive", false) from public.company where id = $1::uuid
`, draft.CompanyID).Scan(&companyActive); err != nil {
		return domain.Service{}, mapServiceRepositoryError(err)
	}
	if !companyActive {
		return domain.Service{}, outbound.ErrRecordNotFound
	}
	if err := tx.QueryRow(queryCtx, `select exists(select 1 from public."client" where id = $1::uuid)`, draft.ClientID).Scan(&clientExists); err != nil {
		return domain.Service{}, fmt.Errorf("validate service client: %w", err)
	}
	if !clientExists {
		return domain.Service{}, outbound.ErrRecordNotFound
	}
	if err := tx.QueryRow(queryCtx, `
select coalesce("isActive", false) from public."serviceCategory" where id = $1::uuid
`, draft.CategoryID).Scan(&categoryActive); err != nil {
		return domain.Service{}, mapServiceRepositoryError(err)
	}
	if !categoryActive {
		return domain.Service{}, outbound.ErrRecordNotFound
	}
	if draft.TechnicianID != nil {
		var validTechnician bool
		if err := tx.QueryRow(queryCtx, `
select exists (
    select 1
    from public."user" as u
    join public.user_company_memberships as m on m.user_id = u.id
    where u.id = $1::uuid
      and u.role::text = 'TECHNICIAN'
      and coalesce(u."isActive", false)
      and m.company_id = $2::uuid
      and m.is_active
)
`, *draft.TechnicianID, draft.CompanyID).Scan(&validTechnician); err != nil {
			return domain.Service{}, fmt.Errorf("validate assigned technician: %w", err)
		}
		if !validTechnician {
			return domain.Service{}, outbound.ErrRecordNotFound
		}
	}

	var serviceID string
	if err := tx.QueryRow(queryCtx, `
insert into public."service"
    ("companyId", "clientId", "categoryId", "technicianId", "createdById",
     "scheduledDate", address, locality, observation, "expectedAmount", "finalAmount", status)
values ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid,
        $6, $7, $8, $9, $10, $11, 'PENDING')
returning id::text
`, draft.CompanyID, draft.ClientID, draft.CategoryID, draft.TechnicianID, draft.CreatedByID,
		draft.ScheduledDate, draft.Address, draft.Locality, draft.Observation, draft.ExpectedAmount, draft.FinalAmount).Scan(&serviceID); err != nil {
		return domain.Service{}, mapServiceRepositoryError(err)
	}
	metadata, err := json.Marshal(map[string]string{
		"clientId": draft.ClientID, "technicianId": stringValueOrEmpty(draft.TechnicianID),
	})
	if err != nil {
		return domain.Service{}, fmt.Errorf("encode service creation audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: serviceID, UserID: draft.CreatedByID, Action: "SERVICE_CREATED",
		Description: "Servicio creado", Metadata: metadata,
	}); err != nil {
		return domain.Service{}, err
	}
	if draft.TechnicianID != nil {
		assigned, err := json.Marshal(map[string]string{"technicianId": *draft.TechnicianID})
		if err != nil {
			return domain.Service{}, fmt.Errorf("encode technician assignment audit: %w", err)
		}
		if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
			ServiceID: serviceID, UserID: draft.CreatedByID, Action: "SERVICE_TECHNICIAN_ASSIGNED",
			Description: "Técnico asignado", Metadata: assigned,
		}); err != nil {
			return domain.Service{}, err
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Service{}, fmt.Errorf("commit service creation: %w", err)
	}
	return r.GetService(ctx, serviceID, domain.ServiceActor{
		UserID: draft.CreatedByID, Role: "ADMIN", CompanyIDs: []string{draft.CompanyID},
	})
}

func (r *ServiceRepository) UpdateService(ctx context.Context, id string, patch domain.ServicePatch, actor domain.ServiceActor) (domain.Service, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.pool.Begin(queryCtx)
	if err != nil {
		return domain.Service{}, fmt.Errorf("begin service update: %w", err)
	}
	defer rollback(tx)

	var status, currentCompanyID string
	var currentTechnicianID pgtype.Text
	if err := tx.QueryRow(queryCtx, `
select s.status::text, s."technicianId"::text, s."companyId"::text
from public."service" as s
where s.id = $1::uuid
  and s."companyId"::text = any($2::text[])
for update
`, id, actor.CompanyIDs).Scan(&status, &currentTechnicianID, &currentCompanyID); err != nil {
		return domain.Service{}, mapServiceRepositoryError(err)
	}
	if status == "COMPLETED" || status == "CANCELLED" || status == "CLOSED" {
		return domain.Service{}, outbound.ErrConflict
	}
	targetCompanyID := currentCompanyID
	if patch.CompanyID.Set && patch.CompanyID.Value != nil {
		targetCompanyID = *patch.CompanyID.Value
		if !containsID(actor.CompanyIDs, *patch.CompanyID.Value) {
			return domain.Service{}, outbound.ErrRecordNotFound
		}
		var active bool
		if err := tx.QueryRow(queryCtx, `
select coalesce("isActive", false) from public.company where id = $1::uuid
`, *patch.CompanyID.Value).Scan(&active); err != nil {
			return domain.Service{}, mapServiceRepositoryError(err)
		}
		if !active {
			return domain.Service{}, outbound.ErrRecordNotFound
		}
	}
	targetTechnicianID := currentTechnicianID.String
	if patch.TechnicianID.Set {
		targetTechnicianID = stringValueOrEmpty(patch.TechnicianID.Value)
	}
	if targetTechnicianID != "" && (patch.CompanyID.Set || patch.TechnicianID.Set) {
		var validTechnician bool
		if err := tx.QueryRow(queryCtx, `
select exists (
    select 1
    from public."user" as u
    join public.user_company_memberships as m on m.user_id = u.id
    where u.id = $1::uuid
      and u.role::text = 'TECHNICIAN'
      and coalesce(u."isActive", false)
      and m.company_id = $2::uuid
      and m.is_active
)
`, targetTechnicianID, targetCompanyID).Scan(&validTechnician); err != nil {
			return domain.Service{}, fmt.Errorf("validate assigned technician: %w", err)
		}
		if !validTechnician {
			return domain.Service{}, outbound.ErrRecordNotFound
		}
	}
	query, args, fields := buildServiceUpdateQuery(id, patch, actor.CompanyIDs)
	if query == "" {
		return domain.Service{}, outbound.ErrConflict
	}
	result, err := tx.Exec(queryCtx, query, args...)
	if err != nil {
		return domain.Service{}, mapServiceRepositoryError(err)
	}
	if result.RowsAffected() == 0 {
		return domain.Service{}, outbound.ErrConflict
	}
	metadata, err := json.Marshal(map[string][]string{"fields": fields})
	if err != nil {
		return domain.Service{}, fmt.Errorf("encode service update audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: id, UserID: actor.UserID, Action: "SERVICE_UPDATED",
		Description: "Servicio actualizado", Metadata: metadata,
	}); err != nil {
		return domain.Service{}, err
	}
	nextTechnicianID := currentTechnicianID.String
	if patch.TechnicianID.Set {
		nextTechnicianID = stringValueOrEmpty(patch.TechnicianID.Value)
	}
	if patch.TechnicianID.Set && nextTechnicianID != currentTechnicianID.String {
		assignment, err := json.Marshal(map[string]string{
			"from": currentTechnicianID.String, "to": nextTechnicianID,
		})
		if err != nil {
			return domain.Service{}, fmt.Errorf("encode service assignment audit: %w", err)
		}
		if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
			ServiceID: id, UserID: actor.UserID, Action: "SERVICE_TECHNICIAN_ASSIGNED",
			Description: "Técnico modificado", Metadata: assignment,
		}); err != nil {
			return domain.Service{}, err
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Service{}, fmt.Errorf("commit service update: %w", err)
	}
	return r.GetService(ctx, id, actor)
}

func (r *ServiceRepository) ChangeServiceStatus(ctx context.Context, id, status, userID string, actor domain.ServiceActor) (domain.Service, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.pool.Begin(queryCtx)
	if err != nil {
		return domain.Service{}, fmt.Errorf("begin service status change: %w", err)
	}
	defer rollback(tx)

	var currentStatus, technicianID string
	if err := tx.QueryRow(queryCtx, `
select s.status::text, coalesce(s."technicianId"::text, '')
from public."service" as s
where s.id = $1::uuid
  and s."companyId"::text = any($2::text[])
  and ($3 <> 'TECHNICIAN' or s."technicianId"::text = $4)
for update
`, id, actor.CompanyIDs, actor.Role, actor.UserID).Scan(&currentStatus, &technicianID); err != nil {
		return domain.Service{}, mapServiceRepositoryError(err)
	}
	if !validServiceTransition(currentStatus, status, actor.Role, technicianID == actor.UserID) {
		return domain.Service{}, outbound.ErrConflict
	}
	result, err := tx.Exec(queryCtx, `
update public."service" as s
set status = $2, "updatedAt" = now()
where s.id = $1::uuid and s.status::text = $3
`, id, status, currentStatus)
	if err != nil {
		return domain.Service{}, mapServiceRepositoryError(err)
	}
	if result.RowsAffected() == 0 {
		return domain.Service{}, outbound.ErrConflict
	}
	metadata, err := json.Marshal(map[string]string{"from": currentStatus, "to": status})
	if err != nil {
		return domain.Service{}, fmt.Errorf("encode status audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: id, UserID: userID, Action: "SERVICE_STATUS_CHANGED",
		Description: "Estado cambiado a " + status, Metadata: metadata,
	}); err != nil {
		return domain.Service{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Service{}, fmt.Errorf("commit service status change: %w", err)
	}
	return r.GetService(ctx, id, actor)
}

func (r *ServiceRepository) FinishService(ctx context.Context, id string, finish domain.ServiceFinish, actor domain.ServiceActor) (domain.Service, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.pool.Begin(queryCtx)
	if err != nil {
		return domain.Service{}, fmt.Errorf("begin service completion: %w", err)
	}
	defer rollback(tx)

	var status, technicianID string
	if err := tx.QueryRow(queryCtx, `
select s.status::text, s."technicianId"::text
from public."service" as s
where s.id = $1::uuid
  and s."companyId"::text = any($2::text[])
  and s."technicianId"::text = $3
for update
`, id, actor.CompanyIDs, actor.UserID).Scan(&status, &technicianID); err != nil {
		return domain.Service{}, mapServiceRepositoryError(err)
	}
	if status != "IN_PROGRESS" || technicianID != actor.UserID {
		return domain.Service{}, outbound.ErrConflict
	}
	var paymentExists bool
	if err := tx.QueryRow(queryCtx, `
select exists (
    select 1
    from public.payment as p
    where p."serviceId" = $1::uuid
)
`, id).Scan(&paymentExists); err != nil {
		return domain.Service{}, fmt.Errorf("check service payment prerequisite: %w", err)
	}
	if !paymentExists {
		return domain.Service{}, outbound.ErrConflict
	}
	result, err := tx.Exec(queryCtx, `
update public."service" as s
set status = 'COMPLETED',
    "finalAmount" = $2,
    observation = coalesce($3, s.observation),
    "completedAt" = now(),
    "closedById" = $4::uuid,
    "updatedAt" = now()
where s.id = $1::uuid and s.status::text = 'IN_PROGRESS'
`, id, finish.FinalAmount, finish.Observation, actor.UserID)
	if err != nil {
		return domain.Service{}, mapServiceRepositoryError(err)
	}
	if result.RowsAffected() == 0 {
		return domain.Service{}, outbound.ErrConflict
	}
	metadata, err := json.Marshal(map[string]string{"from": status, "to": "COMPLETED"})
	if err != nil {
		return domain.Service{}, fmt.Errorf("encode completion audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: id, UserID: actor.UserID, Action: "SERVICE_STATUS_CHANGED",
		Description: "Servicio completado", Metadata: metadata,
	}); err != nil {
		return domain.Service{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Service{}, fmt.Errorf("commit service completion: %w", err)
	}
	return r.GetService(ctx, id, actor)
}

func (r *ServiceRepository) ListServiceHistory(ctx context.Context, id string, actor domain.ServiceActor, page, pageSize int) ([]domain.ActivityLog, int, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	var visible bool
	if err := r.pool.QueryRow(queryCtx, `
select exists (
    select 1
    from public."service" as s
    where s.id = $1::uuid
      and s."companyId"::text = any($2::text[])
      and ($3 <> 'TECHNICIAN' or s."technicianId"::text = $4)
)
`, id, actor.CompanyIDs, actor.Role, actor.UserID).Scan(&visible); err != nil {
		return nil, 0, fmt.Errorf("authorize service history: %w", err)
	}
	if !visible {
		return nil, 0, outbound.ErrRecordNotFound
	}
	var total int
	if err := r.pool.QueryRow(queryCtx, `select count(*) from public."activityLog" where "serviceId" = $1::uuid`, id).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count service history: %w", err)
	}
	rows, err := r.pool.Query(queryCtx, `
select a.id::text, a."serviceId"::text, a."userId"::text, a.action,
       a.description, a.metadata, a."createdAt",
       u.id::text, u.name, u.role::text
from public."activityLog" as a
join public."user" as u on u.id = a."userId"
where a."serviceId" = $1::uuid
order by a."createdAt" asc, a.id asc
limit $2 offset $3
`, id, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("query service history: %w", err)
	}
	defer rows.Close()
	history := make([]domain.ActivityLog, 0)
	for rows.Next() {
		var log domain.ActivityLog
		var metadata []byte
		if err := rows.Scan(
			&log.ID, &log.ServiceID, &log.UserID, &log.Action,
			&log.Description, &metadata, &log.CreatedAt,
			&log.User.ID, &log.User.Name, &log.User.Role,
		); err != nil {
			return nil, 0, fmt.Errorf("scan service history: %w", err)
		}
		if len(metadata) > 0 {
			var value any
			if err := json.Unmarshal(metadata, &value); err != nil {
				return nil, 0, fmt.Errorf("decode service history metadata: %w", err)
			}
			log.Metadata = value
		}
		history = append(history, log)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read service history: %w", err)
	}
	return history, total, nil
}

const serviceSelectSQL = `
select
    s.id::text, s."orderNumber", s."companyId"::text, s."clientId"::text,
    s."categoryId"::text, s."technicianId"::text, s."createdById"::text,
    s."closedById"::text, s.status::text, s."scheduledDate", s.address,
    s.locality, s.observation, s."expectedAmount"::text, s."finalAmount"::text,
    s."completedPhotoUrl", s."completedAt", s."closedAt", s."createdAt", s."updatedAt",
    co.name, coalesce(co."isActive", false),
    cl.id::text, cl.name, cl.email, cl.phone, cl.address, cl.locality,
    cl."createdAt", cl."updatedAt",
    cat.id::text, cat.name, coalesce(cat."isActive", false),
    tech.id::text, tech.name, tech.email, tech.phone, tech.dni,
    tech.role::text, coalesce(tech."isActive", false), tech.avatar,
    creator.id::text, creator.name, creator.email, creator.phone, creator.dni,
    creator.role::text, coalesce(creator."isActive", false), creator.avatar,
    closer.id::text, closer.name, closer.email, closer.phone, closer.dni,
    closer.role::text, coalesce(closer."isActive", false), closer.avatar
from public."service" as s
join public.company as co on co.id = s."companyId"
join public."client" as cl on cl.id = s."clientId"
join public."serviceCategory" as cat on cat.id = s."categoryId"
left join public."user" as tech on tech.id = s."technicianId"
join public."user" as creator on creator.id = s."createdById"
left join public."user" as closer on closer.id = s."closedById"
`

func scanService(row pgx.Row) (domain.Service, error) {
	var service domain.Service
	var technicianID, closedByID, observation, expectedAmount, photoURL pgtype.Text
	var completedAt, closedAt pgtype.Timestamptz
	var clientName, clientEmail, clientAddress, clientLocality pgtype.Text
	var technician, creator, closer serviceUserScan
	if err := row.Scan(
		&service.ID, &service.OrderNumber, &service.CompanyID, &service.ClientID,
		&service.CategoryID, &technicianID, &service.CreatedByID, &closedByID,
		&service.Status, &service.ScheduledDate, &service.Address, &service.Locality,
		&observation, &expectedAmount, &service.FinalAmount, &photoURL,
		&completedAt, &closedAt, &service.CreatedAt, &service.UpdatedAt,
		&service.Company.Name, &service.Company.IsActive,
		&service.Client.ID, &clientName, &clientEmail, &service.Client.Phone,
		&clientAddress, &clientLocality, &service.Client.CreatedAt, &service.Client.UpdatedAt,
		&service.Category.ID, &service.Category.Name, &service.Category.IsActive,
		&technician.ID, &technician.Name, &technician.Email, &technician.Phone, &technician.DNI,
		&technician.Role, &technician.Active, &technician.Avatar,
		&creator.ID, &creator.Name, &creator.Email, &creator.Phone, &creator.DNI,
		&creator.Role, &creator.Active, &creator.Avatar,
		&closer.ID, &closer.Name, &closer.Email, &closer.Phone, &closer.DNI,
		&closer.Role, &closer.Active, &closer.Avatar,
	); err != nil {
		return domain.Service{}, err
	}
	service.TechnicianID = textPointer(technicianID)
	service.ClosedByID = textPointer(closedByID)
	service.Observation = textPointer(observation)
	service.ExpectedAmount = textPointer(expectedAmount)
	service.CompletedPhotoURL = textPointer(photoURL)
	if completedAt.Valid {
		service.CompletedAt = &completedAt.Time
	}
	if closedAt.Valid {
		service.ClosedAt = &closedAt.Time
	}
	service.Client.Name = textPointer(clientName)
	service.Client.Email = textPointer(clientEmail)
	service.Client.Address = textPointer(clientAddress)
	service.Client.Locality = textPointer(clientLocality)
	service.Company.ID = service.CompanyID
	if technician.ID.Valid {
		service.Technician = technician.domain()
	}
	service.CreatedBy = creator.domainValue()
	if closer.ID.Valid {
		value := closer.domainValue()
		service.ClosedBy = &value
	}
	return service, nil
}

type serviceUserScan struct {
	ID     pgtype.Text
	Name   pgtype.Text
	Email  pgtype.Text
	Phone  pgtype.Text
	DNI    pgtype.Text
	Role   pgtype.Text
	Active bool
	Avatar pgtype.Text
}

func (u serviceUserScan) domain() *domain.ServiceUser {
	value := u.domainValue()
	return &value
}

func (u serviceUserScan) domainValue() domain.ServiceUser {
	return domain.ServiceUser{
		ID: u.ID.String, Name: u.Name.String, Email: u.Email.String,
		Phone: textPointer(u.Phone), DNI: textPointer(u.DNI), Role: u.Role.String,
		Active: u.Active, Avatar: textPointer(u.Avatar),
	}
}

func serviceListWhere(filter domain.ServiceFilter) (string, []any) {
	args := []any{filter.Actor.CompanyIDs, filter.Actor.Role, filter.Actor.UserID}
	conditions := []string{
		`s."companyId"::text = any($1::text[])`,
		`($2 <> 'TECHNICIAN' or s."technicianId"::text = $3)`,
	}
	appendCondition := func(expression string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(expression, len(args)))
	}
	if filter.Status != "" {
		appendCondition(`s.status::text = $%d`, filter.Status)
	}
	if filter.TechnicianID != "" && filter.Actor.Role == "ADMIN" {
		appendCondition(`s."technicianId"::text = $%d`, filter.TechnicianID)
	}
	if filter.ClientID != "" {
		appendCondition(`s."clientId"::text = $%d`, filter.ClientID)
	}
	if filter.CompanyID != "" {
		appendCondition(`s."companyId"::text = $%d`, filter.CompanyID)
	}
	if filter.CategoryID != "" {
		appendCondition(`s."categoryId"::text = $%d`, filter.CategoryID)
	}
	if filter.DateFrom != nil {
		appendCondition(`s."scheduledDate" >= $%d`, *filter.DateFrom)
	}
	if filter.DateTo != nil {
		appendCondition(`s."scheduledDate" <= $%d`, *filter.DateTo)
	}
	return strings.Join(conditions, " and "), args
}

func buildServiceUpdateQuery(id string, patch domain.ServicePatch, companyIDs []string) (string, []any, []string) {
	type updateField struct {
		name  string
		value any
	}
	fields := make([]updateField, 0, 10)
	names := make([]string, 0, 10)
	add := func(name string, value any) {
		fields = append(fields, updateField{name: name, value: value})
		names = append(names, name)
	}
	if patch.CompanyID.Set {
		add("companyId", nullableString(patch.CompanyID))
	}
	if patch.ClientID.Set {
		add("clientId", nullableString(patch.ClientID))
	}
	if patch.CategoryID.Set {
		add("categoryId", nullableString(patch.CategoryID))
	}
	if patch.TechnicianID.Set {
		add("technicianId", nullableString(patch.TechnicianID))
	}
	if patch.ScheduledDate.Set {
		add("scheduledDate", patch.ScheduledDate.Value)
	}
	if patch.Address.Set {
		add("address", patch.Address.Value)
	}
	if patch.Locality.Set {
		add("locality", patch.Locality.Value)
	}
	if patch.Observation.Set {
		add("observation", nullableString(patch.Observation))
	}
	if patch.ExpectedAmount.Set {
		add("expectedAmount", nullableString(patch.ExpectedAmount))
	}
	if patch.FinalAmount.Set {
		add("finalAmount", patch.FinalAmount.Value)
	}
	if len(fields) == 0 {
		return "", nil, nil
	}
	args := make([]any, 0, len(fields)+3)
	sets := make([]string, 0, len(fields)+1)
	for _, field := range fields {
		args = append(args, field.value)
		sets = append(sets, fmt.Sprintf("%q = $%d", field.name, len(args)))
	}
	sets = append(sets, `"updatedAt" = now()`)
	args = append(args, id, companyIDs)
	idArg := len(args) - 1
	companyArg := len(args)
	query := fmt.Sprintf(`
update public."service" as s
set %s
where s.id = $%d::uuid
  and s."companyId"::text = any($%d::text[])
  and s.status::text not in ('COMPLETED', 'CANCELLED', 'CLOSED')
`, strings.Join(sets, ", "), idArg, companyArg)
	return query, args, names
}

func insertActivity(ctx context.Context, tx pgx.Tx, record domain.ActivityRecord) error {
	if _, err := tx.Exec(ctx, `
insert into public."activityLog" ("serviceId", "userId", action, description, metadata)
values ($1::uuid, $2::uuid, $3, $4, $5::jsonb)
`, record.ServiceID, record.UserID, record.Action, record.Description, record.Metadata); err != nil {
		return fmt.Errorf("write service activity log: %w", err)
	}
	return nil
}

func validServiceTransition(from, to, role string, isAssignedTechnician bool) bool {
	switch {
	case from == "PENDING" && to == "IN_PROGRESS":
		return role == "ADMIN" || role == "TECHNICIAN" && isAssignedTechnician
	case (from == "PENDING" || from == "IN_PROGRESS") && to == "CANCELLED":
		return role == "ADMIN"
	default:
		return false
	}
}

func stringValueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func containsID(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}

func mapServiceRepositoryError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return outbound.ErrRecordNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return outbound.ErrConflict
		case "23503", "22P02":
			return outbound.ErrRecordNotFound
		case "23514":
			return outbound.ErrConflict
		}
	}
	return fmt.Errorf("service repository operation: %w", err)
}

func (r *ServiceRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.ServiceRepository = (*ServiceRepository)(nil)
