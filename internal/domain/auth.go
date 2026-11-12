package domain

import "time"

type User struct {
	ID         string
	Email      string
	Name       string
	Role       string
	Avatar     string
	Phone      string
	DNI        string
	Active     bool
	CompanyIDs []string `json:"-"`
}

type CompanyMembership struct {
	CompanyID string
	Active    bool
}

type AuthSession struct {
	TokenHash     string
	CSRFTokenHash string
	UserID        string
	User          User
	Memberships   []CompanyMembership
	CreatedAt     time.Time
	ExpiresAt     time.Time
	RevokedAt     *time.Time
	LastSeenAt    *time.Time
}
