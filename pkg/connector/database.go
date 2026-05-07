package connector

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/oauth2"
	"maunium.net/go/mautrix/bridgev2/database"
)

// AuthType identifies which credential mechanism is in use for an email account.
const (
	AuthTypePassword   = "password"    // legacy: SMTP+IMAP via app password / normal password
	AuthTypeOAuthGmail = "oauth-gmail" // Gmail / Workspace via Google OAuth (device flow)
)

// OAuthProvider identifiers persisted in EmailAccount.OAuthProvider.
const (
	OAuthProviderGoogle    = "google"
	OAuthProviderMicrosoft = "microsoft" // reserved for v2; not yet emitted
)

// EmailAccount represents a stored email account with credentials.
//
// Authentication is one of two modes, indicated by AuthType:
//
//   - AuthTypePassword (default for backward compat): Password is an app
//     password used with PLAIN auth on SMTP and IMAP.
//   - AuthTypeOAuthGmail: OAuthRefreshToken / OAuthAccessToken / OAuthExpiry
//     hold the encrypted Google OAuth tokens. Password is unused.
type EmailAccount struct {
	UserMXID         string    `json:"user_mxid"`
	Email            string    `json:"email"`
	Username         string    `json:"username"`
	Password         string    `json:"password"`
	Host             string    `json:"host"`
	Port             int       `json:"port"`
	TLS              bool      `json:"tls"`
	CreatedAt        time.Time `json:"created_at"`
	LastSyncTime     time.Time `json:"last_sync_time"`
	MonitoredFolders []string  `json:"monitored_folders"` // List of folders to monitor (e.g., ["INBOX", "BridgeToBeeper"])

	// AuthType is "password" or "oauth-gmail". Empty value reads as "password"
	// for legacy rows.
	AuthType string `json:"auth_type"`

	// OAuth fields populated when AuthType == AuthTypeOAuthGmail. Tokens stored
	// in the database are AES-GCM encrypted; the in-memory struct holds plaintext
	// after a successful Load*. Expiry is the access-token expiry as time.Time.
	OAuthProvider     string    `json:"oauth_provider,omitempty"`
	OAuthRefreshToken string    `json:"oauth_refresh_token,omitempty"`
	OAuthAccessToken  string    `json:"oauth_access_token,omitempty"`
	OAuthExpiry       time.Time `json:"oauth_expiry,omitempty"`
}

// GetMonitoredFoldersJSON serializes MonitoredFolders to JSON for database storage
func (a *EmailAccount) GetMonitoredFoldersJSON() string {
	if len(a.MonitoredFolders) == 0 {
		return `["INBOX"]` // Default to INBOX if not set
	}
	data, err := json.Marshal(a.MonitoredFolders)
	if err != nil {
		return `["INBOX"]`
	}
	return string(data)
}

// SetMonitoredFoldersFromJSON deserializes MonitoredFolders from JSON database storage
func (a *EmailAccount) SetMonitoredFoldersFromJSON(jsonStr string) {
	if jsonStr == "" {
		a.MonitoredFolders = []string{"INBOX"}
		return
	}
	if err := json.Unmarshal([]byte(jsonStr), &a.MonitoredFolders); err != nil {
		a.MonitoredFolders = []string{"INBOX"}
	}
}

// EmailAccountQuery handles database operations for email accounts
type EmailAccountQuery struct {
	DB *database.Database
}

// --- Minimal AES-GCM helper and key management (self-contained) ---

const encPrefix = "v2:"

const (
	pbkdf2Iterations = 100000 // PBKDF2 iterations for key derivation
	saltSize         = 32     // Salt size in bytes
)

var (
	keyOnce sync.Once
	dbKey   []byte
	keyErr  error
)

func getDBKey() ([]byte, error) {
	keyOnce.Do(func() {
		// Step 1: Check environment variable (highest priority for production)
		passphrase := strings.TrimSpace(os.Getenv("MATRIMAIL_PASSPHRASE"))

		// Step 2: Check for passphrase file if env var not set
		if passphrase == "" {
			passphrase, _ = readPassphraseFile()
		}

		// Step 3: Auto-generate secure passphrase if neither exists
		if passphrase == "" {
			passphrase, keyErr = generateAndStorePassphrase()
			if keyErr != nil {
				return
			}
		}

		salt, err := getSalt()
		if err != nil {
			keyErr = fmt.Errorf("failed to get salt: %w", err)
			return
		}

		// Derive key using PBKDF2
		dbKey = pbkdf2.Key([]byte(passphrase), salt, pbkdf2Iterations, 32, sha256.New)
	})
	if keyErr != nil {
		return nil, keyErr
	}
	if len(dbKey) != 32 {
		return nil, fmt.Errorf("derived key must be 32 bytes, got %d", len(dbKey))
	}
	return dbKey, nil
}

// getUserConfigDir returns the user's config directory for cross-platform support
func getUserConfigDir() (string, error) {
	// Check XDG_CONFIG_HOME first (Linux/Unix)
	if configDir := os.Getenv("XDG_CONFIG_HOME"); configDir != "" {
		return filepath.Join(configDir, "matrimail"), nil
	}

	// Get user home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}

	// Platform-specific config paths
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(homeDir, "AppData", "Roaming", "Matrimail"), nil
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", "Matrimail"), nil
	default: // Linux and other Unix-like systems
		return filepath.Join(homeDir, ".config", "matrimail"), nil
	}
}

// getPassphraseFilePath returns the path to the passphrase file
func getPassphraseFilePath() (string, error) {
	configDir, err := getUserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "passphrase"), nil
}

// readPassphraseFile reads passphrase from the user config file
func readPassphraseFile() (string, error) {
	passphrasePath, err := getPassphraseFilePath()
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(passphrasePath)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}

// generateAndStorePassphrase creates a new secure passphrase and stores it
func generateAndStorePassphrase() (string, error) {
	// Generate 32 random bytes for a secure passphrase
	randomBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, randomBytes); err != nil {
		return "", fmt.Errorf("failed to generate random passphrase: %w", err)
	}

	// Encode as base64 for storage
	passphrase := base64.StdEncoding.EncodeToString(randomBytes)

	// Get passphrase file path
	passphrasePath, err := getPassphraseFilePath()
	if err != nil {
		return "", err
	}

	// Create config directory with secure permissions
	configDir := filepath.Dir(passphrasePath)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create config directory: %w", err)
	}

	// Write passphrase file with secure permissions
	if err := os.WriteFile(passphrasePath, []byte(passphrase), 0o600); err != nil {
		return "", fmt.Errorf("failed to write passphrase file: %w", err)
	}

	// Log to stderr to avoid potential information disclosure in stdout logs
	fmt.Fprintf(os.Stderr, "Auto-generated secure passphrase stored (check config directory)\n")
	fmt.Fprintf(os.Stderr, "Matrimail is ready! Your credentials will be securely encrypted.\n")

	return passphrase, nil
}

// getSalt returns the salt for PBKDF2, generating one if needed
func getSalt() ([]byte, error) {
	// Use absolute path for security - prevent directory traversal attacks
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}
	dataDir := filepath.Join(cwd, "data")
	saltPath := filepath.Join(dataDir, "matrimail.salt")

	// Try to read existing salt
	if data, err := os.ReadFile(saltPath); err == nil {
		salt, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err == nil && len(salt) == saltSize {
			return salt, nil
		}
	}

	// Generate new salt
	salt := make([]byte, saltSize)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("failed to generate salt: %w", err)
	}

	// Create directory with secure permissions
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	// Save salt
	saltB64 := base64.StdEncoding.EncodeToString(salt)
	if err := os.WriteFile(saltPath, []byte(saltB64), 0o600); err != nil {
		return nil, fmt.Errorf("failed to save salt: %w", err)
	}

	return salt, nil
}

func encryptString(plain string) (string, error) {
	key, err := getDBKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, []byte(plain), nil)
	buf := append(nonce, ct...)
	return encPrefix + base64.StdEncoding.EncodeToString(buf), nil
}

func decryptString(stored string) (string, error) {
	// Check for old v1 encrypted data and provide helpful error message
	if strings.HasPrefix(stored, "v1:") {
		return "", errors.New("cannot decrypt old v1 encrypted data - please delete your database and reconfigure your email accounts with the new secure system")
	}

	// Only accept v2 encrypted data
	if !strings.HasPrefix(stored, encPrefix) {
		return "", errors.New("value is not encrypted with expected v2: prefix")
	}

	key, err := getDBKey()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, encPrefix))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce := raw[:gcm.NonceSize()]
	ct := raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

func (eaq *EmailAccountQuery) CreateTable(ctx context.Context) error {
	// Create table. New columns (auth_type and the oauth_* group) are NULLable
	// for the migration path on existing databases; the ALTER statements below
	// take care of adding them when CREATE TABLE IF NOT EXISTS finds an older
	// schema already in place.
	_, err := eaq.DB.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS email_accounts (
			user_mxid TEXT NOT NULL,
			email TEXT NOT NULL,
			username TEXT NOT NULL,
			password TEXT NOT NULL,
			host TEXT NOT NULL,
			port INTEGER NOT NULL,
			tls BOOLEAN NOT NULL,
			created_at TIMESTAMP NOT NULL,
			last_sync_time TIMESTAMP,
			monitored_folders TEXT DEFAULT '["INBOX"]',
			auth_type TEXT NOT NULL DEFAULT 'password',
			oauth_provider TEXT,
			oauth_refresh_token TEXT,
			oauth_access_token TEXT,
			oauth_expiry INTEGER,
			PRIMARY KEY (user_mxid, email)
		)
	`)
	if err != nil {
		return err
	}

	// Migration ALTERs: SQLite returns an error when the column already exists,
	// which we deliberately swallow. Each statement is independent so a single
	// already-applied migration doesn't block the rest.
	for _, stmt := range []string{
		`ALTER TABLE email_accounts ADD COLUMN monitored_folders TEXT DEFAULT '["INBOX"]'`,
		`ALTER TABLE email_accounts ADD COLUMN auth_type TEXT NOT NULL DEFAULT 'password'`,
		`ALTER TABLE email_accounts ADD COLUMN oauth_provider TEXT`,
		`ALTER TABLE email_accounts ADD COLUMN oauth_refresh_token TEXT`,
		`ALTER TABLE email_accounts ADD COLUMN oauth_access_token TEXT`,
		`ALTER TABLE email_accounts ADD COLUMN oauth_expiry INTEGER`,
	} {
		_, _ = eaq.DB.Exec(ctx, stmt)
	}

	// Create performance indexes
	_, err = eaq.DB.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_email_accounts_user_created
		ON email_accounts(user_mxid, created_at)
	`)
	if err != nil {
		return err
	}

	_, err = eaq.DB.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_email_accounts_last_sync
		ON email_accounts(user_mxid, last_sync_time)
	`)

	return err
}

// SaveOAuthToken persists the OAuth refresh + access token for the given
// account, encrypting both tokens at rest with the same AES-GCM key used for
// passwords. Sets auth_type='oauth-gmail' and oauth_provider=provider.
//
// The account row must already exist (created by the IMAP login flow). This
// method does NOT create the row — it updates it in place.
func (eaq *EmailAccountQuery) SaveOAuthToken(ctx context.Context, userMXID, email, provider string, tok *oauth2.Token) error {
	if tok == nil {
		return errors.New("SaveOAuthToken: nil token")
	}
	if provider == "" {
		provider = OAuthProviderGoogle
	}

	encRefresh, err := encryptString(tok.RefreshToken)
	if err != nil {
		return fmt.Errorf("encrypt refresh token: %w", err)
	}
	encAccess, err := encryptString(tok.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}

	expiryNanos := tok.Expiry.UnixNano()
	if tok.Expiry.IsZero() {
		expiryNanos = 0
	}

	authType := AuthTypeOAuthGmail
	res, err := eaq.DB.Exec(ctx, dialectQuery(eaq.DB.Dialect, `
		UPDATE email_accounts
		SET auth_type = ?, oauth_provider = ?, oauth_refresh_token = ?, oauth_access_token = ?, oauth_expiry = ?
		WHERE user_mxid = ? AND email = ?
	`), authType, provider, encRefresh, encAccess, expiryNanos, userMXID, email)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("SaveOAuthToken: no email_accounts row for %s/%s", userMXID, email)
	}
	return nil
}

// LoadOAuthToken returns the saved OAuth token for the account. If the account
// is not in OAuth mode (auth_type != "oauth-gmail") it returns ("", nil, nil)
// so callers can fall back to the password path.
func (eaq *EmailAccountQuery) LoadOAuthToken(ctx context.Context, userMXID, email string) (string, *oauth2.Token, error) {
	rows, err := eaq.DB.Query(ctx, dialectQuery(eaq.DB.Dialect, `
		SELECT auth_type, COALESCE(oauth_provider, ''), COALESCE(oauth_refresh_token, ''),
		       COALESCE(oauth_access_token, ''), COALESCE(oauth_expiry, 0)
		FROM email_accounts
		WHERE user_mxid = ? AND email = ?
	`), userMXID, email)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		if rerr := rows.Err(); rerr != nil {
			return "", nil, rerr
		}
		return "", nil, nil
	}

	var authType, provider, encRefresh, encAccess string
	var expiryNanos sql.NullInt64
	if err := rows.Scan(&authType, &provider, &encRefresh, &encAccess, &expiryNanos); err != nil {
		return "", nil, err
	}

	if authType != AuthTypeOAuthGmail {
		return "", nil, nil
	}
	if encRefresh == "" {
		return provider, nil, errors.New("LoadOAuthToken: account is oauth-gmail but has no refresh token stored")
	}

	refresh, err := decryptString(encRefresh)
	if err != nil {
		return provider, nil, fmt.Errorf("decrypt refresh token: %w", err)
	}
	var access string
	if encAccess != "" {
		access, err = decryptString(encAccess)
		if err != nil {
			// Access token may be missing or stale; that's fine, the refresh
			// token will mint a new one.
			access = ""
		}
	}

	tok := &oauth2.Token{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
	}
	if expiryNanos.Valid && expiryNanos.Int64 > 0 {
		tok.Expiry = time.Unix(0, expiryNanos.Int64)
	}
	return provider, tok, nil
}

func (eaq *EmailAccountQuery) GetAccount(ctx context.Context, userMXID, email string) (*EmailAccount, error) {
	account := &EmailAccount{}
	rows, err := eaq.DB.Query(ctx, dialectQuery(eaq.DB.Dialect, `
		SELECT user_mxid, email, username, password, host, port, tls, created_at, last_sync_time, COALESCE(monitored_folders, '["INBOX"]')
		FROM email_accounts
		WHERE user_mxid = ? AND email = ?
	`), userMXID, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		// Check if Next() failed due to an error
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("database query failed: %w", err)
		}
		return nil, nil // No account found
	}

	var monitoredFoldersJSON string
	err = rows.Scan(
		&account.UserMXID, &account.Email, &account.Username, &account.Password,
		&account.Host, &account.Port, &account.TLS, &account.CreatedAt, &account.LastSyncTime,
		&monitoredFoldersJSON,
	)
	if err != nil {
		return nil, err
	}
	account.SetMonitoredFoldersFromJSON(monitoredFoldersJSON)
	// Decrypt password (fresh deployments always store encrypted)
	plain, derr := decryptString(account.Password)
	if derr != nil {
		// Don't expose decryption details to prevent information disclosure
		return nil, fmt.Errorf("failed to decrypt stored credentials")
	}
	account.Password = plain
	return account, nil
}

func (eaq *EmailAccountQuery) GetUserAccounts(ctx context.Context, userMXID string) ([]*EmailAccount, error) {
	rows, err := eaq.DB.Query(ctx, dialectQuery(eaq.DB.Dialect, `
		SELECT user_mxid, email, username, password, host, port, tls, created_at, last_sync_time, COALESCE(monitored_folders, '["INBOX"]')
		FROM email_accounts
		WHERE user_mxid = ?
		ORDER BY created_at ASC
	`), userMXID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []*EmailAccount
	for rows.Next() {
		account := &EmailAccount{}
		var monitoredFoldersJSON string
		err = rows.Scan(
			&account.UserMXID, &account.Email, &account.Username, &account.Password,
			&account.Host, &account.Port, &account.TLS, &account.CreatedAt, &account.LastSyncTime,
			&monitoredFoldersJSON,
		)
		if err != nil {
			return nil, err
		}
		account.SetMonitoredFoldersFromJSON(monitoredFoldersJSON)
		plain, derr := decryptString(account.Password)
		if derr != nil {
			// Don't expose decryption details to prevent information disclosure
			return nil, fmt.Errorf("failed to decrypt stored credentials")
		}
		account.Password = plain
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

// GetUserAccountsBasic returns user accounts without decrypting passwords (for display/status)
func (eaq *EmailAccountQuery) GetUserAccountsBasic(ctx context.Context, userMXID string) ([]*EmailAccount, error) {
	rows, err := eaq.DB.Query(ctx, dialectQuery(eaq.DB.Dialect, `
		SELECT user_mxid, email, username, host, port, tls, created_at, last_sync_time, COALESCE(monitored_folders, '["INBOX"]')
		FROM email_accounts
		WHERE user_mxid = ?
		ORDER BY created_at ASC
	`), userMXID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Pre-allocate slice with reasonable capacity to reduce reallocations
	accounts := make([]*EmailAccount, 0, 4)
	for rows.Next() {
		account := &EmailAccount{}
		var monitoredFoldersJSON string
		err = rows.Scan(
			&account.UserMXID, &account.Email, &account.Username,
			&account.Host, &account.Port, &account.TLS, &account.CreatedAt, &account.LastSyncTime,
			&monitoredFoldersJSON,
		)
		if err != nil {
			return nil, err
		}
		account.SetMonitoredFoldersFromJSON(monitoredFoldersJSON)
		// Password is left empty for basic account info
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (eaq *EmailAccountQuery) UpsertAccount(ctx context.Context, account *EmailAccount) error {
	enc, err := encryptString(account.Password)
	if err != nil {
		return fmt.Errorf("failed to encrypt password: %w", err)
	}
	// ON CONFLICT DO UPDATE works on both SQLite 3.24+ and Postgres 9.5+.
	// Note: this UPSERT only touches the password-flow columns; auth_type and
	// oauth_* columns are managed separately via SaveOAuthToken/UpdateMonitoredFolders.
	_, err = eaq.DB.Exec(ctx, dialectQuery(eaq.DB.Dialect, `
		INSERT INTO email_accounts
		(user_mxid, email, username, password, host, port, tls, created_at, last_sync_time, monitored_folders)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_mxid, email) DO UPDATE SET
			username = EXCLUDED.username,
			password = EXCLUDED.password,
			host = EXCLUDED.host,
			port = EXCLUDED.port,
			tls = EXCLUDED.tls,
			last_sync_time = EXCLUDED.last_sync_time,
			monitored_folders = EXCLUDED.monitored_folders
	`), account.UserMXID, account.Email, account.Username, enc,
		account.Host, account.Port, account.TLS, account.CreatedAt, account.LastSyncTime,
		account.GetMonitoredFoldersJSON())
	return err
}

func (eaq *EmailAccountQuery) DeleteAccount(ctx context.Context, userMXID, email string) error {
	_, err := eaq.DB.Exec(ctx, dialectQuery(eaq.DB.Dialect, `
		DELETE FROM email_accounts
		WHERE user_mxid = ? AND email = ?
	`), userMXID, email)
	return err
}

func (eaq *EmailAccountQuery) UpdateLastSync(ctx context.Context, userMXID, email string, syncTime time.Time) error {
	_, err := eaq.DB.Exec(ctx, dialectQuery(eaq.DB.Dialect, `
		UPDATE email_accounts
		SET last_sync_time = ?
		WHERE user_mxid = ? AND email = ?
	`), syncTime, userMXID, email)
	return err
}

// UpdateMonitoredFolders persists a fresh folder list onto an existing
// account row without going through INSERT OR REPLACE (which would erase any
// auth_type / oauth_* columns set on the side via SaveOAuthToken). Used by
// the OAuth login flow's completeLogin step.
func (eaq *EmailAccountQuery) UpdateMonitoredFolders(ctx context.Context, userMXID, email string, folders []string) error {
	tmp := &EmailAccount{MonitoredFolders: folders}
	_, err := eaq.DB.Exec(ctx, dialectQuery(eaq.DB.Dialect, `
		UPDATE email_accounts
		SET monitored_folders = ?
		WHERE user_mxid = ? AND email = ?
	`), tmp.GetMonitoredFoldersJSON(), userMXID, email)
	return err
}
