package application

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrTechnicianForbidden   = errors.New("technician operation forbidden")
	ErrInvalidTechnician     = errors.New("invalid technician input")
	ErrTechnicianNotFound    = outbound.ErrRecordNotFound
	ErrTechnicianConflict    = outbound.ErrConflict
	ErrTechnicianUnavailable = outbound.ErrIdentityProviderUnavailable
)

const (
	minTechnicianPasswordLength = 8
	defaultTechnicianPageSize   = 25
	maxTechnicianPageSize       = 100
)

type TechnicianUseCases struct {
	repository  outbound.TechnicianRepository
	provisioner outbound.UserProvisioner
}

type TechnicianCreateInput struct {
	Name       string
	Email      string
	Password   string
	Phone      *string
	DNI        *string
	CompanyIDs []string
}

type TechnicianListInput struct {
	Page     int
	PageSize int
}

type TechnicianListResult struct {
	Data     []domain.Technician `json:"data"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"pageSize"`
	Total    int                 `json:"total"`
}

func NewTechnicianUseCases(repository outbound.TechnicianRepository, provisioner outbound.UserProvisioner) *TechnicianUseCases {
	return &TechnicianUseCases{repository: repository, provisioner: provisioner}
}

func (s *TechnicianUseCases) List(ctx context.Context, user domain.User, input TechnicianListInput) (TechnicianListResult, error) {
	filter, err := technicianReadScope(user)
	if err != nil {
		return TechnicianListResult{}, err
	}
	page := input.Page
	if page == 0 {
		page = 1
	}
	pageSize := input.PageSize
	if pageSize == 0 {
		pageSize = defaultTechnicianPageSize
	}
	if page < 1 || pageSize < 1 || pageSize > maxTechnicianPageSize ||
		page > int(^uint(0)>>1)/pageSize {
		return TechnicianListResult{}, ErrInvalidTechnician
	}
	filter.Page = page
	filter.PageSize = pageSize
	technicians, total, err := s.repository.ListTechnicians(ctx, filter)
	if err != nil {
		return TechnicianListResult{}, err
	}
	if technicians == nil {
		technicians = []domain.Technician{}
	}
	return TechnicianListResult{Data: technicians, Page: page, PageSize: pageSize, Total: total}, nil
}

func (s *TechnicianUseCases) Get(ctx context.Context, user domain.User, id string) (domain.Technician, error) {
	if !validUUID(id) {
		return domain.Technician{}, ErrTechnicianNotFound
	}
	filter, err := technicianReadScope(user)
	if err != nil {
		return domain.Technician{}, err
	}
	if !strings.EqualFold(user.Role, "ADMIN") && id != user.ID {
		return domain.Technician{}, ErrTechnicianForbidden
	}
	return s.repository.GetTechnician(ctx, id, filter)
}

func (s *TechnicianUseCases) Create(ctx context.Context, user domain.User, input TechnicianCreateInput) (domain.Technician, error) {
	if err := requireTechnicianAdmin(user); err != nil {
		return domain.Technician{}, err
	}
	input, err := normalizeTechnicianCreate(input, user.CompanyIDs)
	if err != nil {
		return domain.Technician{}, err
	}
	if s.provisioner == nil {
		return domain.Technician{}, ErrTechnicianUnavailable
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return domain.Technician{}, ErrTechnicianUnavailable
	}
	subject, err := s.provisioner.ProvisionUser(ctx, input.Email, input.Password)
	if err != nil {
		return domain.Technician{}, err
	}
	if subject == "" {
		return domain.Technician{}, ErrTechnicianUnavailable
	}
	return s.repository.CreateTechnician(ctx, domain.TechnicianDraft{
		Name:         input.Name,
		Email:        input.Email,
		PasswordHash: string(passwordHash),
		Phone:        input.Phone,
		DNI:          input.DNI,
		CompanyIDs:   input.CompanyIDs,
		IdentityID:   subject,
	})
}

func (s *TechnicianUseCases) Update(ctx context.Context, user domain.User, id string, patch domain.TechnicianPatch) (domain.Technician, error) {
	if err := requireTechnicianAdmin(user); err != nil {
		return domain.Technician{}, err
	}
	if !validUUID(id) {
		return domain.Technician{}, ErrTechnicianNotFound
	}
	if !patch.Name.Set && !patch.Phone.Set && !patch.DNI.Set && !patch.Active.Set &&
		!patch.Avatar.Set && !patch.CompanyIDs.Set {
		return domain.Technician{}, ErrInvalidTechnician
	}
	if patch.Name.Set {
		if patch.Name.Value == nil {
			return domain.Technician{}, ErrInvalidTechnician
		}
		normalized := strings.TrimSpace(*patch.Name.Value)
		if normalized == "" || utf8.RuneCountInString(normalized) > 160 {
			return domain.Technician{}, ErrInvalidTechnician
		}
		patch.Name.Value = &normalized
	}
	for _, field := range []*domain.Field[string]{&patch.Phone, &patch.DNI, &patch.Avatar} {
		if field.Set && field.Value != nil {
			normalized := strings.TrimSpace(*field.Value)
			if normalized == "" {
				field.Value = nil
			} else {
				field.Value = &normalized
			}
		}
	}
	if patch.CompanyIDs.Set {
		if patch.CompanyIDs.Value == nil {
			return domain.Technician{}, ErrInvalidTechnician
		}
		companyIDs, ok := authorizedCompanyIDs(*patch.CompanyIDs.Value, user.CompanyIDs)
		if !ok || len(companyIDs) == 0 {
			return domain.Technician{}, ErrInvalidTechnician
		}
		patch.CompanyIDs.Value = &companyIDs
	}
	return s.repository.UpdateTechnician(ctx, id, patch, user.CompanyIDs)
}

func technicianReadScope(user domain.User) (domain.TechnicianFilter, error) {
	if !user.Active || (!strings.EqualFold(user.Role, "ADMIN") && !strings.EqualFold(user.Role, "TECHNICIAN")) {
		return domain.TechnicianFilter{}, ErrTechnicianForbidden
	}
	return domain.TechnicianFilter{
		UserID: user.ID, CompanyIDs: append([]string(nil), user.CompanyIDs...),
		SelfOnly: strings.EqualFold(user.Role, "TECHNICIAN"),
	}, nil
}

func requireTechnicianAdmin(user domain.User) error {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return ErrTechnicianForbidden
	}
	return nil
}

func normalizeTechnicianCreate(input TechnicianCreateInput, allowedCompanyIDs []string) (TechnicianCreateInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 160 ||
		len(input.Password) < minTechnicianPasswordLength {
		return TechnicianCreateInput{}, ErrInvalidTechnician
	}
	email, err := mail.ParseAddress(input.Email)
	if err != nil || email.Address != input.Email {
		return TechnicianCreateInput{}, ErrInvalidTechnician
	}
	input.Phone = normalizeNullableString(input.Phone)
	input.DNI = normalizeNullableString(input.DNI)
	if input.Phone != nil && utf8.RuneCountInString(*input.Phone) > 80 ||
		input.DNI != nil && utf8.RuneCountInString(*input.DNI) > 80 {
		return TechnicianCreateInput{}, ErrInvalidTechnician
	}
	if len(input.CompanyIDs) == 0 {
		input.CompanyIDs = append([]string(nil), allowedCompanyIDs...)
	}
	companyIDs, ok := authorizedCompanyIDs(input.CompanyIDs, allowedCompanyIDs)
	if !ok || len(companyIDs) == 0 {
		return TechnicianCreateInput{}, ErrInvalidTechnician
	}
	input.CompanyIDs = companyIDs
	return input, nil
}

func authorizedCompanyIDs(requested, allowed []string) ([]string, bool) {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		if validUUID(id) {
			allowedSet[strings.ToLower(id)] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(requested))
	result := make([]string, 0, len(requested))
	for _, id := range requested {
		if !validUUID(id) {
			return nil, false
		}
		normalized := strings.ToLower(id)
		if _, ok := allowedSet[normalized]; !ok {
			return nil, false
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result, true
}
