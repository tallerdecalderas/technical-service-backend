// Command seed provisions the first admin user (and optional technicians) for local development.
//
// It creates the identity in Supabase Auth, the internal user row,
// a default company, the company membership, and the identity link —
// all five steps from docs/.backend/authentication.md in one command.
//
// Usage:
//
//	go run ./cmd/seed \
//	  -name  "Admin"                \
//	  -email "tallerdecalderasok@gmail.com"     \
//	  -pass  "MiPassword123!"       \
//	  -company "Taller de calderas"          \
//	  -role  ADMIN
//
// Additional technicians can be created under the same company with -technician
// (repeatable). A bare `go run ./cmd/seed` works with all defaults.
//
// At the end, with -verify, it attempts a real Supabase password grant
// (auth/v1/token?grant_type=password) to confirm you can log in.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"technical-service-backend/internal/config"
)

type technician struct {
	name   string
	email  string
	pass   string
	role   string
	authID string // filled after Supabase Auth creation
}

func main() {
	name := flag.String("name", "Admin", "Nombre del usuario principal")
	email := flag.String("email", "tallerdecalderasok@gmail.com", "Email (credencial de login)")
	password := flag.String("pass", "Admin123!", "Contraseña (mín 6 chars)")
	companyName := flag.String("company", "Taller de calderas", "Nombre de la empresa")
	role := flag.String("role", "ADMIN", "Rol: ADMIN o TECHNICIAN")
	technicians := flag.String("technician", "", "Técnico adicional: nombre,email,contraseña (se puede repetir)")
	verify := flag.Bool("verify", false, "Al final, intenta un login real con Supabase para validar credenciales")
	flag.Parse()

	*role = strings.ToUpper(*role)
	if *role != "ADMIN" && *role != "TECHNICIAN" {
		log.Fatal("❌ -role debe ser ADMIN o TECHNICIAN")
	}
	if len(*password) < 6 {
		log.Fatal("❌ -pass debe tener al menos 6 caracteres")
	}

	// Build the list of users to provision: principal first, then additional technicians.
	users := []technician{
		{name: *name, email: *email, pass: *password, role: *role},
	}
	for _, raw := range strings.Split(*technicians, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.SplitN(raw, ",", 3)
		if len(parts) != 3 {
			log.Fatalf("❌ -technician debe ser 'nombre,email,contraseña', recibí: %q", raw)
		}
		t := technician{
			name:  strings.TrimSpace(parts[0]),
			email: strings.TrimSpace(parts[1]),
			pass:  strings.TrimSpace(parts[2]),
			role:  "TECHNICIAN",
		}
		if t.name == "" || t.email == "" || len(t.pass) < 6 {
			log.Fatalf("❌ Técnico inválido %q: nombre y email requeridos, contraseña mín 6 chars", raw)
		}
		users = append(users, t)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Error cargando configuración: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	httpClient := &http.Client{Timeout: 15 * time.Second}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Println("  🌱 SEED — aprovisionamiento automático de usuarios")
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Println()

	// --- 1. Create identities in Supabase Auth (one per user) ---
	fmt.Println("🔐 Paso 1/N: Creando identidades en Supabase Auth...")
	for i := range users {
		u := &users[i]
		authUID, err := ensureSupabaseUser(ctx, cfg.Auth.SupabaseURL, cfg.Auth.SupabaseAPIKey, httpClient, u.email, u.pass)
		if err != nil {
			log.Fatalf("❌ Error en Supabase Auth para %s: %v", u.email, err)
		}
		u.authID = authUID
		fmt.Printf("   ✅ %s — Auth UUID: %s\n", u.email, authUID)
	}

	// --- 2-N. Database inserts (in one transaction) ---
	pool, err := pgxpool.New(ctx, cfg.DatabaseRuntimeURL)
	if err != nil {
		log.Fatalf("❌ Error conectando a PostgreSQL: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("❌ No se pudo conectar a la base de datos: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatalf("❌ Error iniciando transacción: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Company: create once, reuse for every user.
	fmt.Println("🏢 Paso 2/N: Creando empresa...")
	var companyID string
	err = tx.QueryRow(ctx, `select id::text from public.company where name = $1 limit 1`, *companyName).Scan(&companyID)
	if err != nil {
		err = tx.QueryRow(ctx, `insert into public.company (name, "isActive") values ($1, true) returning id::text`, *companyName).Scan(&companyID)
		if err != nil {
			log.Fatalf("❌ Error creando empresa: %v", err)
		}
		fmt.Printf("   ✅ Empresa creada — %s (ID: %s)\n", *companyName, companyID)
	} else {
		fmt.Printf("   ✅ Empresa existente — %s (ID: %s)\n", *companyName, companyID)
	}

	for i := range users {
		u := &users[i]

		fmt.Printf("👤 Paso 3/%d: Creando usuario interno %s...\n", i+4, u.email)
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(u.pass), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("❌ Error generando hash para %s: %v", u.email, err)
		}

		var userID string
		// Idempotent: update if the email already exists.
		err = tx.QueryRow(ctx, `
			insert into public."user" (name, email, role, "passwordHash", "isActive")
			values ($1, $2, $3::text, $4, true)
			on conflict (email) do update set
				name = excluded.name,
				role = excluded.role::text,
				"passwordHash" = excluded."passwordHash",
				"isActive" = true
			returning id::text
		`, u.name, u.email, u.role, string(passwordHash)).Scan(&userID)
		if err != nil {
			log.Fatalf("❌ Error creando usuario %s: %v", u.email, err)
		}
		fmt.Printf("   ✅ Usuario interno — %s (ID: %s)\n", u.email, userID)

		fmt.Printf("🔗 Paso 4/%d: Vinculando %s ↔ empresa...\n", i+5, u.email)
		_, err = tx.Exec(ctx, `
			insert into public.user_company_memberships (user_id, company_id, is_active)
			values ($1::uuid, $2::uuid, true)
			on conflict (user_id, company_id) do update set is_active = true
		`, userID, companyID)
		if err != nil {
			log.Fatalf("❌ Error creando membresía para %s: %v", u.email, err)
		}
		fmt.Println("   ✅ Membresía activa")

		fmt.Printf("🪪 Paso 5/%d: Vinculando Supabase ↔ usuario interno %s...\n", i+6, u.email)
		_, err = tx.Exec(ctx, `
			insert into public.user_identities (user_id, provider, provider_subject, active)
			values ($1::uuid, 'supabase', $2, true)
			on conflict (user_id, provider) do update set
				provider_subject = excluded.provider_subject,
				active = true
		`, userID, u.authID)
		if err != nil {
			log.Fatalf("❌ Error vinculando identidad para %s: %v", u.email, err)
		}
		fmt.Println("   ✅ Identidad vinculada")
	}

	if err := tx.Commit(ctx); err != nil {
		log.Fatalf("❌ Error confirmando transacción: %v", err)
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Println("  ✅ Aprovisionamiento completado")
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Printf("  Empresa:   %s\n", *companyName)
	for i := range users {
		u := &users[i]
		fmt.Printf("  [%d] %-20s %-30s rol=%s\n", i+1, u.name, u.email, u.role)
	}
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Println()

	// --- Optional: verify login via Supabase password grant ---
	if *verify {
		fmt.Println("🔑 Verificando login (Supabase password grant)...")
		for i := range users {
			u := &users[i]
			tokens, err := supabaseAuthenticate(ctx, cfg.Auth.SupabaseURL, cfg.Auth.SupabaseAPIKey, httpClient, u.email, u.pass)
			if err != nil {
				log.Fatalf("❌ Login falló para %s: %v", u.email, err)
			}
			fmt.Printf("   ✅ %s — token obtenido (exp %s)\n", u.email, tokens.ExpiresAt.UTC().Format("15:04:05"))
		}
		fmt.Println()
		fmt.Println("  Credenciales válidas. Ya podés iniciar sesión en el frontend 🚀")
	} else {
		fmt.Println("  Usá -verify para probar un login real con Supabase.")
		fmt.Println()
		fmt.Printf("  Email de prueba: %s\n", users[0].email)
		fmt.Printf("  Contraseña:      %s\n", users[0].pass)
		fmt.Println("  Ya podés iniciar sesión en el frontend 🚀")
	}
}

// ensureSupabaseUser creates a user in Supabase Auth using the service_role key.
// Idempotent: if the email already exists, it looks up the existing auth UUID
// from the admin list endpoint so the command can be re-run safely.
func ensureSupabaseUser(ctx context.Context, supabaseURL, apiKey string, client *http.Client, email, password string) (string, error) {
	// Try creating with the admin endpoint (auto-confirms the user).
	authUID, err := createSupabaseUser(ctx, supabaseURL, apiKey, email, password)
	if err == nil {
		return authUID, nil
	}
	// If it already exists, fetch the existing UUID by email.
	if !strings.Contains(err.Error(), "ya existe en Supabase Auth") {
		return "", err
	}
	return lookupSupabaseUserByEmail(ctx, supabaseURL, apiKey, client, email)
}

// createSupabaseUser calls Supabase Auth Admin API to create a new user.
// It uses the service_role key (apiKey) to bypass email confirmation.
func createSupabaseUser(ctx context.Context, supabaseURL, apiKey, email, password string) (string, error) {
	payload, err := json.Marshal(map[string]interface{}{
		"email":         email,
		"password":      password,
		"email_confirm": true,
	})
	if err != nil {
		return "", fmt.Errorf("encoding request: %w", err)
	}

	endpoint := strings.TrimRight(supabaseURL, "/") + "/auth/v1/admin/users"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("apikey", apiKey)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnprocessableEntity || resp.StatusCode == http.StatusConflict {
		return "", fmt.Errorf("el email '%s' ya existe en Supabase Auth.\n"+
			"Si querés recrearlo, eliminalo primero desde el dashboard de Supabase → Authentication → Users", email)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Supabase Auth respondió HTTP %d: %s\n\n"+
			"💡 Asegurate de que SUPABASE_AUTH_API_KEY sea la service_role key (no la anon key).\n"+
			"   La encontrás en: Supabase Dashboard → Settings → API → service_role", resp.StatusCode, string(body))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.ID == "" {
		return "", fmt.Errorf("respuesta inesperada de Supabase Auth: %s", string(body))
	}
	return result.ID, nil
}

// lookupSupabaseUserByEmail lists admin users and returns the UUID matching email.
func lookupSupabaseUserByEmail(ctx context.Context, supabaseURL, apiKey string, client *http.Client, email string) (string, error) {
	endpoint := strings.TrimRight(supabaseURL, "/") + "/auth/v1/admin/users"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("apikey", apiKey)
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("Supabase Auth respondió HTTP %d: %s", resp.StatusCode, string(body))
	}

	var list []struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list); err != nil {
		return "", fmt.Errorf("decoding users list: %w", err)
	}
	for _, u := range list {
		if strings.EqualFold(u.Email, email) {
			return u.ID, nil
		}
	}
	return "", fmt.Errorf("el email '%s' existe en Supabase Auth pero no se pudo obtener su UUID", email)
}

// IdentityTokens mirrors outbound.IdentityTokens without importing the app layer.
type IdentityTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// supabaseAuthenticate performs a real password grant against Supabase Auth
// (auth/v1/token?grant_type=password) to validate that the credentials work.
func supabaseAuthenticate(ctx context.Context, supabaseURL, apiKey string, client *http.Client, email, password string) (IdentityTokens, error) {
	payload, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		return IdentityTokens{}, fmt.Errorf("encode request: %w", err)
	}

	endpoint := strings.TrimRight(supabaseURL, "/") + "/auth/v1/token?grant_type=password"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return IdentityTokens{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("apikey", apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return IdentityTokens{}, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return IdentityTokens{}, fmt.Errorf("Supabase Auth respondió HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.AccessToken == "" || result.RefreshToken == "" {
		return IdentityTokens{}, fmt.Errorf("respuesta inesperada de Supabase Auth: %s", string(body))
	}

	return IdentityTokens{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(result.ExpiresIn) * time.Second),
	}, nil
}

func init() {
	// Suppress default log prefix
	log.SetFlags(0)

	// Warn if SUPABASE_AUTH_API_KEY looks like the anon key; we need service_role
	if key := os.Getenv("SUPABASE_AUTH_API_KEY"); key != "" && len(key) < 100 {
		fmt.Println()
		fmt.Println("⚠️  SUPABASE_AUTH_API_KEY parece ser la anon key (es muy corta).")
		fmt.Println("   Este comando necesita la service_role key para crear usuarios.")
		fmt.Println("   Encontrala en: Supabase Dashboard → Settings → API → service_role")
		fmt.Println()
	}
}
