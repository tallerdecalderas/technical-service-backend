// Command seed provisions the first admin user (and optional technicians) for local development.
//
// It creates the identity in Supabase Auth, the internal user row,
// a default company, the company membership, and the identity link —
// all five steps from docs/.backend/authentication.md in one command.
//
// Usage:
//
//	go run ./cmd/seed \
//	  -name "Admin" \
//	  -email "admin@example.com" \
//	  -pass "una-contraseña-segura" \
//	  -company "Taller de calderas" \
//	  -role ADMIN
//
// Additional technicians can be created under the same company with -technician
// (repeat the flag). Use -skip-primary to add technicians without also
// provisioning the primary admin. Set SUPABASE_AUTH_ADMIN_KEY to a secret API
// key before running this command; SUPABASE_AUTH_API_KEY remains the publishable
// key used for password-grant verification.
//
// At the end, with -verify, it attempts a real Supabase password grant
// (auth/v1/token?grant_type=password) to confirm you can log in.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
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

type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

var errSupabaseUserAlreadyExists = errors.New("Supabase Auth user already exists")

func main() {
	name := flag.String("name", "Admin", "Nombre del usuario principal")
	email := flag.String("email", "tallerdecalderasok@gmail.com", "Email (credencial de login)")
	password := flag.String("pass", "", "Contraseña (mín 6 chars)")
	companyName := flag.String("company", "Taller de calderas", "Nombre de la empresa")
	role := flag.String("role", "ADMIN", "Rol: ADMIN o TECHNICIAN")
	skipPrimary := flag.Bool("skip-primary", false, "No crear/actualizar el usuario principal; usar con -technician")
	var technicians stringList
	flag.Var(&technicians, "technician", "Técnico adicional: nombre,email,contraseña (repetible)")
	verify := flag.Bool("verify", false, "Al final, intenta un login real con Supabase para validar credenciales")
	flag.Parse()

	*role = strings.ToUpper(*role)
	if *role != "ADMIN" && *role != "TECHNICIAN" {
		log.Fatal("❌ -role debe ser ADMIN o TECHNICIAN")
	}
	if strings.TrimSpace(*companyName) == "" {
		log.Fatal("❌ -company no puede estar vacío")
	}
	*companyName = strings.TrimSpace(*companyName)

	// Build the list of users to provision: principal first, then additional technicians.
	users := make([]technician, 0, len(technicians)+1)
	if !*skipPrimary {
		if strings.TrimSpace(*name) == "" || strings.TrimSpace(*email) == "" {
			log.Fatal("❌ -name y -email no pueden estar vacíos")
		}
		if len(*password) < 6 {
			log.Fatal("❌ -pass es obligatorio y debe tener al menos 6 caracteres")
		}
		*name = strings.TrimSpace(*name)
		*email = strings.ToLower(strings.TrimSpace(*email))
		users = append(users, technician{name: *name, email: *email, pass: *password, role: *role})
	}
	if *skipPrimary && len(technicians) == 0 {
		log.Fatal("❌ -skip-primary requiere al menos un -technician")
	}
	for _, raw := range technicians {
		raw = strings.TrimSpace(raw)
		parts := strings.SplitN(raw, ",", 3)
		if len(parts) != 3 {
			log.Fatalf("❌ -technician debe ser 'nombre,email,contraseña', recibí: %q", raw)
		}
		t := technician{
			name:  strings.TrimSpace(parts[0]),
			email: strings.ToLower(strings.TrimSpace(parts[1])),
			pass:  parts[2],
			role:  "TECHNICIAN",
		}
		if t.name == "" || t.email == "" || len(t.pass) < 6 {
			log.Fatalf("❌ Técnico inválido %q: nombre y email requeridos, contraseña mín 6 chars", raw)
		}
		users = append(users, t)
	}
	if len(users) == 0 {
		log.Fatal("❌ no hay usuarios para aprovisionar")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Error cargando configuración: %v", err)
	}
	adminAPIKey, err := supabaseAdminAPIKeyFromEnvironment()
	if err != nil {
		log.Fatalf("❌ Error de configuración para crear usuarios: %v", err)
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
		authUID, err := ensureSupabaseUser(ctx, cfg.Auth.SupabaseURL, adminAPIKey, httpClient, u.email, u.pass)
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
		err = tx.QueryRow(ctx, `
			insert into public.company (id, name, "isActive")
			values (gen_random_uuid(), $1, true)
			returning id::text
		`, *companyName).Scan(&companyID)
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
			on conflict (provider, provider_subject) do update set
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
		if !*skipPrimary {
			fmt.Printf("  Email de prueba: %s\n", users[0].email)
		}
		fmt.Println("  La contraseña no se muestra por seguridad.")
	}
}

func supabaseAdminAPIKeyFromEnvironment() (string, error) {
	for _, name := range []string{"SUPABASE_AUTH_ADMIN_KEY", "SUPABASE_SECRET_KEY"} {
		if key := strings.TrimSpace(os.Getenv(name)); key != "" {
			return key, nil
		}
	}

	// Keep compatibility with the key name already present in some local .env files.
	if raw := strings.TrimSpace(os.Getenv("SUPABASE_SECRET_KEYS")); raw != "" {
		if strings.HasPrefix(raw, "{") {
			var keys map[string]string
			if err := json.Unmarshal([]byte(raw), &keys); err != nil {
				return "", fmt.Errorf("SUPABASE_SECRET_KEYS no contiene un JSON válido")
			}
			if key := strings.TrimSpace(keys["default"]); key != "" {
				return key, nil
			}
			if len(keys) == 1 {
				for _, key := range keys {
					if strings.TrimSpace(key) != "" {
						return strings.TrimSpace(key), nil
					}
				}
			}
			return "", fmt.Errorf("configurá SUPABASE_AUTH_ADMIN_KEY para elegir una clave secreta")
		}
		return raw, nil
	}

	if key := strings.TrimSpace(os.Getenv("SUPABASE_SERVICE_ROLE_KEY")); key != "" {
		return key, nil
	}

	return "", fmt.Errorf("falta SUPABASE_AUTH_ADMIN_KEY (clave secreta de Supabase; no uses la publishable)")
}

// ensureSupabaseUser creates a user in Supabase Auth using a server-only key.
// Idempotent: if the email already exists, it looks up the existing auth UUID
// from the admin list endpoint so the command can be re-run safely.
func ensureSupabaseUser(ctx context.Context, supabaseURL, apiKey string, client *http.Client, email, password string) (string, error) {
	// Try creating with the admin endpoint (auto-confirms the user).
	authUID, err := createSupabaseUser(ctx, supabaseURL, apiKey, client, email, password)
	if err == nil {
		return authUID, nil
	}
	// If it already exists, fetch the existing UUID by email.
	if !errors.Is(err, errSupabaseUserAlreadyExists) {
		return "", err
	}
	return lookupSupabaseUserByEmail(ctx, supabaseURL, apiKey, client, email)
}

func setSupabaseAdminHeaders(req *http.Request, apiKey string) {
	req.Header.Set("apikey", apiKey)
	// The modern sb_secret key is not a JWT and must not be sent as a bearer token.
	if !strings.HasPrefix(apiKey, "sb_secret_") {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

// createSupabaseUser calls Supabase Auth Admin API to create a confirmed user.
func createSupabaseUser(ctx context.Context, supabaseURL, apiKey string, client *http.Client, email, password string) (string, error) {
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
	setSupabaseAdminHeaders(req, apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusConflict || (resp.StatusCode == http.StatusUnprocessableEntity && isDuplicateUserError(body)) {
		return "", fmt.Errorf("%w: %s", errSupabaseUserAlreadyExists, email)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Supabase Auth respondió HTTP %d: %s\n"+
			"Verificá que SUPABASE_AUTH_ADMIN_KEY sea una clave secreta del mismo proyecto que SUPABASE_URL", resp.StatusCode, string(body))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.ID == "" {
		return "", fmt.Errorf("respuesta inesperada de Supabase Auth: %s", string(body))
	}
	return result.ID, nil
}

func isDuplicateUserError(body []byte) bool {
	lowerBody := strings.ToLower(string(body))
	return strings.Contains(lowerBody, "user_already_exists") ||
		strings.Contains(lowerBody, "email_exists") ||
		strings.Contains(lowerBody, "already registered") ||
		strings.Contains(lowerBody, "already exists")
}

// lookupSupabaseUserByEmail lists admin users and returns the UUID matching email.
func lookupSupabaseUserByEmail(ctx context.Context, supabaseURL, apiKey string, client *http.Client, email string) (string, error) {
	baseURL := strings.TrimRight(supabaseURL, "/") + "/auth/v1/admin/users"
	for page := 1; ; page++ {
		query := url.Values{}
		query.Set("page", fmt.Sprint(page))
		query.Set("per_page", "1000")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"?"+query.Encode(), nil)
		if err != nil {
			return "", fmt.Errorf("creating request: %w", err)
		}
		setSupabaseAdminHeaders(req, apiKey)

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("sending request: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			return "", fmt.Errorf("Supabase Auth respondió HTTP %d al buscar el usuario: %s", resp.StatusCode, string(body))
		}

		var result struct {
			Users []struct {
				ID    string `json:"id"`
				Email string `json:"email"`
			} `json:"users"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return "", fmt.Errorf("decoding users list: %w", decodeErr)
		}
		for _, user := range result.Users {
			if strings.EqualFold(user.Email, email) {
				return user.ID, nil
			}
		}
		if len(result.Users) < 1000 {
			break
		}
	}
	return "", fmt.Errorf("el email '%s' ya existe, pero no se pudo obtener su UUID", email)
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
	log.SetFlags(0)
}
