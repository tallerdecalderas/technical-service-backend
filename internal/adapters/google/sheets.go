package google

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"technical-service-backend/internal/domain"
)

const (
	googleTokenURL   = "https://oauth2.googleapis.com/token"
	googleSheetsAPI  = "https://sheets.googleapis.com/v4/spreadsheets"
	sheetsReadScope  = "https://www.googleapis.com/auth/spreadsheets.readonly"
	sheetsWriteScope = "https://www.googleapis.com/auth/spreadsheets"
)

type serviceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

type SheetsCatalogReader struct {
	credentialsJSON string
	spreadsheetID   string
	valueRange      string
	client          *http.Client
}

func NewSheetsCatalogReader(credentialsJSON, spreadsheetID, valueRange string) *SheetsCatalogReader {
	if strings.TrimSpace(valueRange) == "" {
		valueRange = "A1:Z10000"
	}
	return &SheetsCatalogReader{
		credentialsJSON: credentialsJSON,
		spreadsheetID:   spreadsheetID,
		valueRange:      valueRange,
		client:          &http.Client{Timeout: 20 * time.Second},
	}
}

func (r *SheetsCatalogReader) ReadCatalogRows(ctx context.Context) ([][]string, string, error) {
	if strings.TrimSpace(r.credentialsJSON) == "" || strings.TrimSpace(r.spreadsheetID) == "" {
		return nil, "", errors.New("google sheets catalog source is not configured")
	}
	account, err := parseServiceAccount(r.credentialsJSON)
	if err != nil {
		return nil, "", err
	}
	token, err := fetchAccessToken(ctx, r.client, account, sheetsReadScope)
	if err != nil {
		return nil, "", err
	}
	requestURL := fmt.Sprintf("%s/%s/values/%s?majorDimension=ROWS",
		googleSheetsAPI, url.PathEscape(r.spreadsheetID), url.PathEscape(r.valueRange))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := r.client.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("read google sheet: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, "", fmt.Errorf("read google sheet response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("google sheets returned status %d", response.StatusCode)
	}
	var payload struct {
		Values [][]string `json:"values"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "", fmt.Errorf("decode google sheet: %w", err)
	}
	sourceRef := "sheets:" + r.spreadsheetID + "!" + r.valueRange
	return payload.Values, sourceRef, nil
}

// SheetsScheduleExporter exporta y sincroniza la agenda de servicios hacia Google Sheets (Fase 7).
type SheetsScheduleExporter struct {
	credentialsJSON string
	spreadsheetID   string
	sheetName       string
	client          *http.Client
}

func NewSheetsScheduleExporter(credentialsJSON, spreadsheetID, sheetName string) *SheetsScheduleExporter {
	if strings.TrimSpace(sheetName) == "" {
		sheetName = "Agenda"
	}
	return &SheetsScheduleExporter{
		credentialsJSON: credentialsJSON,
		spreadsheetID:   spreadsheetID,
		sheetName:       sheetName,
		client:          &http.Client{Timeout: 20 * time.Second},
	}
}

func (s *SheetsScheduleExporter) SyncSchedule(ctx context.Context, services []domain.Service) error {
	if strings.TrimSpace(s.credentialsJSON) == "" || strings.TrimSpace(s.spreadsheetID) == "" {
		return errors.New("google sheets schedule target is not configured")
	}
	account, err := parseServiceAccount(s.credentialsJSON)
	if err != nil {
		return err
	}
	token, err := fetchAccessToken(ctx, s.client, account, sheetsWriteScope)
	if err != nil {
		return err
	}

	rows := make([][]any, 0, len(services)+1)
	rows = append(rows, []any{
		"OT", "Fecha y Hora", "Cliente", "Teléfono", "Dirección", "Localidad", "Categoría", "Técnico", "Estado",
	})
	for _, svc := range services {
		techName := "Sin asignar"
		if svc.Technician != nil {
			techName = svc.Technician.Name
		}
		clientName := ""
		if svc.Client.Name != nil {
			clientName = *svc.Client.Name
		}
		rows = append(rows, []any{
			svc.OrderNumber,
			svc.ScheduledDate.Format("2006-01-02 15:04"),
			clientName,
			svc.Client.Phone,
			svc.Address,
			svc.Locality,
			svc.Category.Name,
			techName,
			svc.Status,
		})
	}

	payloadBody, err := json.Marshal(map[string]any{
		"range":  s.sheetName + "!A1:I",
		"values": rows,
	})
	if err != nil {
		return fmt.Errorf("marshal google sheet payload: %w", err)
	}

	requestURL := fmt.Sprintf("%s/%s/values/%s?valueInputOption=USER_ENTERED",
		googleSheetsAPI, url.PathEscape(s.spreadsheetID), url.PathEscape(s.sheetName+"!A1:I"))
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, requestURL, bytes.NewReader(payloadBody))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")

	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("sync google sheet: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("google sheets sync failed with status %d: %s", response.StatusCode, string(body))
	}
	return nil
}

func fetchAccessToken(ctx context.Context, client *http.Client, account serviceAccount, scope string) (string, error) {
	tokenURI := strings.TrimSpace(account.TokenURI)
	if tokenURI == "" {
		tokenURI = googleTokenURL
	}
	assertion, err := serviceAccountJWT(account.ClientEmail, account.PrivateKey, tokenURI, scope)
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("request google access token: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read google access token response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("google access token returned status %d", response.StatusCode)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.AccessToken == "" {
		return "", errors.New("google access token response is invalid")
	}
	return payload.AccessToken, nil
}

func parseServiceAccount(credentialsJSON string) (serviceAccount, error) {
	var account serviceAccount
	raw := strings.TrimSpace(credentialsJSON)
	if !strings.HasPrefix(raw, "{") {
		contents, err := os.ReadFile(raw)
		if err != nil {
			return account, errors.New("google credentials are not a service account JSON or readable file")
		}
		raw = strings.TrimSpace(string(contents))
	}
	if !strings.HasPrefix(raw, "{") {
		return account, errors.New("google credentials must be a service account JSON")
	}
	if err := json.Unmarshal([]byte(raw), &account); err != nil {
		return account, errors.New("google credentials are not valid JSON")
	}
	if account.ClientEmail == "" || account.PrivateKey == "" {
		return account, errors.New("google credentials are incomplete")
	}
	account.PrivateKey = strings.ReplaceAll(account.PrivateKey, `\n`, "\n")
	return account, nil
}

func serviceAccountJWT(clientEmail, privateKey, audience, scope string) (string, error) {
	block, _ := pem.Decode([]byte(privateKey))
	if block == nil {
		return "", errors.New("google private key is not PEM")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", errors.New("google private key is not PKCS8")
	}
	key, ok := parsedKey.(*rsa.PrivateKey)
	if !ok {
		return "", errors.New("google private key is not RSA")
	}
	now := time.Now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iss": clientEmail, "scope": scope, "aud": audience,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign google jwt: %w", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
