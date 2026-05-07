package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	gmail "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/Leicas/matrimail/pkg/email"
	"github.com/Leicas/matrimail/pkg/imap"
)

// Login flow IDs registered with bridgev2.
const (
	LoginFlowIDPassword   = "email-password"
	LoginFlowIDOAuthGmail = "email-oauth-gmail"
)

// EmailLoginProcess represents the email login flow.
//
// Two flows share this struct:
//
//   - LoginFlowIDPassword: classic IMAP-app-password flow. Steps:
//     credentials → folder_selection → confirmation → complete.
//   - LoginFlowIDOAuthGmail: Phase B device-code flow. Steps:
//     oauth_email → oauth_wait → folder_selection → confirmation → complete.
//
// flowID is set by EmailConnector.CreateLogin from the user's flow pick.
type EmailLoginProcess struct {
	user      *bridgev2.User
	connector *EmailConnector
	flowID    string // see LoginFlowID* constants
	email     string
	username  string
	password  string

	// Multi-step flow state
	currentStep      string            // "credentials", "folder_selection", "confirmation"
	availableFolders []imap.FolderInfo // Folders enumerated after credential validation
	selectedFolders  []imap.FolderInfo // User's folder selection
	selectedNames    []string          // Raw IMAP folder names for storage
	providerName     string            // Detected provider name for display
	testClient       *imap.Client      // Validated IMAP client (reused for folder listing)

	// OAuth-flow state — only populated for LoginFlowIDOAuthGmail.
	// dcr is the device-code dance kickoff; pollResult is filled by the
	// background goroutine when the user authorizes (or it errors out).
	oauthOnce   sync.Once
	oauthCtx    context.Context
	oauthCancel context.CancelFunc
	dcr         *email.DeviceCodeResponse
	pollMu      sync.Mutex
	pollDone    bool
	pollToken   *oauth2.Token
	pollErr     error
}

var (
	_ bridgev2.LoginProcess          = (*EmailLoginProcess)(nil)
	_ bridgev2.LoginProcessUserInput = (*EmailLoginProcess)(nil)
)

// EmailLoginMetadata contains email-specific login metadata
type EmailLoginMetadata struct {
	Email    string `json:"email"`
	Username string `json:"username"`
}

// Start begins the login process. The first prompt depends on flowID:
// for LoginFlowIDOAuthGmail we ask for the email up front (so we know which
// account the device-code authorizes), then kick off DeviceCodeStart.
func (elp *EmailLoginProcess) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	if elp.flowID == LoginFlowIDOAuthGmail {
		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeUserInput,
			StepID:       "oauth_email",
			Instructions: elp.buildOAuthEmailPrompt(),
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{{
					Type:        bridgev2.LoginInputFieldTypeEmail,
					ID:          "email",
					Name:        "Email Address",
					Description: "Your Gmail / Workspace address",
				}},
			},
		}, nil
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "credentials",
		Instructions: elp.buildLoginInstructions(),
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type:        bridgev2.LoginInputFieldTypeEmail,
					ID:          "email",
					Name:        "Email Address",
					Description: "Your full email address",
				},
				{
					Type:        bridgev2.LoginInputFieldTypePassword,
					ID:          "password",
					Name:        "Password",
					Description: "Your email password or App Password",
				},
			},
		},
	}, nil
}

// buildOAuthEmailPrompt is the first-step UI for LoginFlowIDOAuthGmail. We
// only need the email here; the OAuth dance happens after.
func (elp *EmailLoginProcess) buildOAuthEmailPrompt() string {
	return `**Matrimail — Gmail OAuth Login**

🔐 Sign in with your Google account, no app password required.

**Step 1 of 3** — enter the Gmail / Google Workspace address you want to bridge.

After you submit, the bridge will hand back a short URL + code; open it in your
browser, sign in to Google, and authorize the matrimail OAuth client. The bridge
will detect the authorization and continue automatically.

*Need help?* ` + "`!matrimail help`"
}

// buildLoginInstructions creates helpful login instructions based on common email providers
func (elp *EmailLoginProcess) buildLoginInstructions() string {
	return `**Matrimail Login**

📧 **Please enter your email credentials using the form fields below.**

**Important Notes:**
• For Gmail/Yahoo/Outlook: Use an **App Password** (not your regular password)
• The bridge will automatically detect your email provider settings
• Your password will be encrypted and stored securely

**App Password Setup Guide:**
📱 **Gmail:** Settings → Security → 2-Step Verification → App passwords
📱 **Yahoo:** Account Info → Account security → Generate app password  
📱 **Outlook:** Security → Sign-in options → App passwords
📱 **iCloud:** Sign-In and Security → App-Specific Passwords

**Popular Providers Supported:**
Gmail, Yahoo, Outlook, iCloud, FastMail - Auto-configured
Custom IMAP servers - Auto-detected

*The bridge will test your IMAP connection automatically after you submit your credentials.*

**Need help?** Use ` + "`!matrimail help`" + ` for more information or ` + "`!matrimail status`" + ` to check connection status.`
}

// Cancel cancels the login process and tears down any in-flight OAuth poll
// goroutine so a re-issued !matrimail login doesn't leak a polling worker.
func (elp *EmailLoginProcess) Cancel() {
	if elp.oauthCancel != nil {
		elp.oauthCancel()
	}
}

// SubmitUserInput handles a login step submission. Routing depends on the
// currentStep, which is advanced explicitly by each handler. The OAuth flow
// shares folder_selection / confirmation with the password flow once the
// token is in hand.
func (elp *EmailLoginProcess) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	switch elp.currentStep {
	case "oauth_wait":
		return elp.handleOAuthWait(ctx, input)
	case "folder_selection":
		return elp.handleFolderSelection(ctx, input)
	case "confirmation":
		return elp.handleConfirmation(ctx, input)
	default:
		if elp.flowID == LoginFlowIDOAuthGmail {
			return elp.handleOAuthEmail(ctx, input)
		}
		return elp.handleCredentials(ctx, input)
	}
}

// handleCredentials validates credentials and proceeds to folder selection
func (elp *EmailLoginProcess) handleCredentials(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	// Extract credentials from user input data
	for key, value := range input {
		switch key {
		case "email":
			elp.email = strings.TrimSpace(value)
		case "password":
			elp.password = strings.TrimSpace(value)
		}
	}

	// Set username to email if not provided separately
	if elp.username == "" {
		elp.username = elp.email
	}

	// Validate required fields
	if elp.email == "" {
		return nil, fmt.Errorf("email address is required")
	}
	if elp.password == "" {
		return nil, fmt.Errorf("password is required")
	}

	// Detect email provider and give specific guidance
	providerInfo := elp.detectEmailProvider()
	if providerInfo != nil {
		elp.providerName = providerInfo.Name
	} else {
		elp.providerName = "Email"
	}

	// Show provider detection results to user
	if providerInfo != nil && providerInfo.Name != "Custom Provider" {
		// Known provider detected
		elp.connector.Bridge.Log.Info().
			Str("provider", providerInfo.Name).
			Str("domain", providerInfo.Domain).
			Msg("Known provider detected, using optimized settings")
	} else {
		// Unknown provider - using auto-detection fallback
		parts := strings.Split(elp.email, "@")
		if len(parts) == 2 {
			domain := parts[1]
			fallbackHost := fmt.Sprintf("imap.%s", domain)
			elp.connector.Bridge.Log.Info().
				Str("domain", domain).
				Str("fallback_host", fallbackHost).
				Msg("Unknown provider detected, attempting auto-detection")
		}
	}

	// Test IMAP connection and keep client for folder listing
	client, err := elp.testIMAPConnectionAndKeep(ctx)
	if err != nil {
		// Provide provider-specific troubleshooting
		errorMsg := elp.buildConnectionErrorMessage(err, providerInfo)
		return nil, fmt.Errorf("%s", errorMsg)
	}
	elp.testClient = client

	// Enumerate available folders
	folders, err := client.ListFolders()
	if err != nil {
		client.Disconnect()
		elp.connector.Bridge.Log.Warn().Err(err).Msg("Failed to list folders, falling back to INBOX only")
		// Fall back to INBOX-only if folder listing fails
		elp.selectedNames = []string{"INBOX"}
		elp.selectedFolders = []imap.FolderInfo{{
			Name:    "INBOX",
			Display: "INBOX",
			Icon:    "📥",
			Type:    imap.FolderTypeStandard,
		}}
		// Skip folder selection, go directly to save
		return elp.completeLogin(ctx)
	}

	elp.availableFolders = folders
	client.Disconnect() // Close connection, will reconnect when starting monitoring

	// If no folders found, fall back to INBOX
	if len(folders) == 0 {
		elp.selectedNames = []string{"INBOX"}
		elp.selectedFolders = []imap.FolderInfo{{
			Name:    "INBOX",
			Display: "INBOX",
			Icon:    "📥",
			Type:    imap.FolderTypeStandard,
		}}
		return elp.completeLogin(ctx)
	}

	// Move to folder selection step
	elp.currentStep = "folder_selection"

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "folder_selection",
		Instructions: BuildFolderSelectionPrompt(folders, elp.providerName),
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type:        bridgev2.LoginInputFieldTypeUsername,
					ID:          "folder_selection",
					Name:        "Folder Selection",
					Description: "Enter folder number(s), 'default' for INBOX, or 'cancel'",
				},
			},
		},
	}, nil
}

// handleFolderSelection validates folder selection and proceeds to confirmation
func (elp *EmailLoginProcess) handleFolderSelection(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	_ = ctx // ctx reserved for future use
	selectionInput := ""
	for key, value := range input {
		if key == "folder_selection" {
			selectionInput = value
			break
		}
	}

	result := ValidateFolderSelection(selectionInput, elp.availableFolders)

	if result.IsCancel {
		return nil, fmt.Errorf("login cancelled by user")
	}

	if !result.Valid {
		// Show error and re-prompt
		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeUserInput,
			StepID:       "folder_selection",
			Instructions: result.ErrorMessage + "\n\n" + BuildFolderSelectionPrompt(elp.availableFolders, elp.providerName),
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{
					{
						Type:        bridgev2.LoginInputFieldTypeUsername,
						ID:          "folder_selection",
						Name:        "Folder Selection",
						Description: "Enter folder number(s), 'default' for INBOX, or 'cancel'",
					},
				},
			},
		}, nil
	}

	// Store selection
	elp.selectedNames = result.SelectedNames
	elp.selectedFolders = result.SelectedInfos

	// Move to confirmation step
	elp.currentStep = "confirmation"

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "confirmation",
		Instructions: BuildConfirmationPrompt(elp.selectedFolders),
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type:        bridgev2.LoginInputFieldTypeUsername,
					ID:          "confirmation",
					Name:        "Confirmation",
					Description: "Type 'yes' to confirm, 'no' to go back, or 'cancel'",
				},
			},
		},
	}, nil
}

// handleConfirmation validates confirmation and completes login
func (elp *EmailLoginProcess) handleConfirmation(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	confirmInput := ""
	for key, value := range input {
		if key == "confirmation" {
			confirmInput = value
			break
		}
	}

	confirmed, goBack, errorMsg := ValidateConfirmation(confirmInput)

	if goBack {
		// Go back to folder selection
		elp.currentStep = "folder_selection"
		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeUserInput,
			StepID:       "folder_selection",
			Instructions: BuildFolderSelectionPrompt(elp.availableFolders, elp.providerName),
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{
					{
						Type:        bridgev2.LoginInputFieldTypeUsername,
						ID:          "folder_selection",
						Name:        "Folder Selection",
						Description: "Enter folder number(s) or 'default' for INBOX",
					},
				},
			},
		}, nil
	}

	if !confirmed {
		// Show error and re-prompt
		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeUserInput,
			StepID:       "confirmation",
			Instructions: errorMsg + "\n\n" + BuildConfirmationPrompt(elp.selectedFolders),
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{
					{
						Type:        bridgev2.LoginInputFieldTypeUsername,
						ID:          "confirmation",
						Name:        "Confirmation",
						Description: "Type 'yes' or 'no'",
					},
				},
			},
		}, nil
	}

	// User confirmed, complete the login
	return elp.completeLogin(ctx)
}

// completeLogin saves the account and completes the login process.
//
// For the OAuth flow we skip the full UpsertAccount (which would clobber the
// auth_type / oauth_* columns under SQLite's INSERT-OR-REPLACE semantics) and
// instead just update the monitored_folders column on the row pre-created at
// the start of the OAuth dance. The OAuth tokens were persisted by
// SaveOAuthToken in handleOAuthWait.
func (elp *EmailLoginProcess) completeLogin(ctx context.Context) (*bridgev2.LoginStep, error) {
	if elp.flowID == LoginFlowIDOAuthGmail {
		if err := elp.connector.DB.UpdateMonitoredFolders(ctx, elp.user.MXID.String(), elp.email, elp.selectedNames); err != nil {
			return nil, fmt.Errorf("failed to save folder selection: %w", err)
		}
	} else {
		// Save credentials to database FIRST (before creating user login)
		// This ensures LoadUserLogin can find the account when it's called
		if err := elp.saveAccount(ctx); err != nil {
			return nil, fmt.Errorf("failed to save account: %w", err)
		}
	}

	// Create new user login using the bridgev2 pattern
	// This will trigger LoadUserLogin which needs the account to exist in DB
	userLoginID := networkid.UserLoginID(fmt.Sprintf("email:%s", elp.email))
	userLogin, err := elp.user.NewLogin(ctx, &database.UserLogin{
		ID:         userLoginID,
		RemoteName: elp.email,
		Metadata: &EmailLoginMetadata{
			Email:    elp.email,
			Username: elp.username,
		},
	}, &bridgev2.NewLoginParams{
		DeleteOnConflict: false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create user login: %w", err)
	}

	// Build success message with folder info
	var folderList strings.Builder
	for _, f := range elp.selectedFolders {
		folderList.WriteString(fmt.Sprintf("\n  • %s %s %s", f.Icon, f.Display, f.TypeBracket()))
	}

	successMsg := fmt.Sprintf(`✅ **Account configured successfully!**

📧 **Email:** %s
📁 **Monitoring:**%s

Emails from the selected folder(s) will now appear in Beeper.
To change folders later, use `+"`!matrimail config folders`"+``, elp.email, folderList.String())

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       "complete",
		Instructions: successMsg,
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: userLogin.ID,
			UserLogin:   userLogin,
		},
	}, nil
}

// EmailUserLogin represents a logged-in email account
type EmailUserLogin struct {
	UserLogin *bridgev2.UserLogin
	connector *EmailConnector
	Email     string
	Password  string
	IMAPHost  string
	IMAPPort  int
	TLS       bool
}

func (eul *EmailUserLogin) Connect(ctx context.Context) error {
	// Connection is handled by the IMAP manager
	return eul.connector.IMAPManager.AddAccount(eul.UserLogin, eul.Email, eul.Email, eul.Password)
}

func (eul *EmailUserLogin) Disconnect() {
	// Disconnection is handled by the IMAP manager
	eul.connector.IMAPManager.RemoveAccount(eul.UserLogin.UserMXID.String(), eul.Email)
}

func (eul *EmailUserLogin) IsLoggedIn() bool {
	// Check if account is active in IMAP manager
	statuses := eul.connector.IMAPManager.GetAccountStatus(eul.UserLogin.UserMXID.String())
	for _, status := range statuses {
		if status.Email == eul.Email {
			return status.Connected
		}
	}
	return false
}

func (eul *EmailUserLogin) GetRemoteID() networkid.UserLoginID {
	return networkid.UserLoginID(fmt.Sprintf("email:%s", eul.Email))
}

func (eul *EmailUserLogin) GetRemoteName() string {
	return eul.Email
}

// testIMAPConnection tests the IMAP connection with provided credentials
// testIMAPConnectionAndKeep tests the IMAP connection and returns the connected client
// for subsequent operations like folder listing
func (elp *EmailLoginProcess) testIMAPConnectionAndKeep(ctx context.Context) (*imap.Client, error) {
	// Keep ctx parameter used to satisfy linters even if not currently leveraged here.
	_ = ctx
	// Create a temporary logger for testing (NO PASSWORDS LOGGED)
	logger := elp.connector.Bridge.Log.With().
		Str("component", "login_test").
		Str("email", elp.email)

	// Safely extract domain for logging
	if parts := strings.Split(elp.email, "@"); len(parts) == 2 {
		logger = logger.Str("host_detected", parts[1])
	}

	finalLogger := logger.Logger()

	// Create test IMAP client without UserLogin (just for testing connection)
	client, err := imap.NewClient(elp.email, elp.username, elp.password, nil, &finalLogger, elp.connector.Config.Logging.Sanitized, elp.connector.Config.Logging.PseudonymSecret, elp.connector.Config.Network.IMAP.StartupBackfillSeconds, elp.connector.Config.Network.IMAP.StartupBackfillMax, elp.connector.Config.Network.IMAP.InitialIdleTimeoutSeconds, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create IMAP client: %w", err)
	}

	// Test connection
	err = client.Connect()
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}

	finalLogger.Info().Msg("IMAP connection test successful")
	return client, nil
}

// saveAccount saves the email account credentials to database
func (elp *EmailLoginProcess) saveAccount(ctx context.Context) error {
	logger := elp.connector.Bridge.Log.With().
		Str("component", "login_save").
		Str("email", elp.email).
		Logger()

	logger.Info().Msg("Saving account credentials to database")

	// Auto-detect provider settings for saving
	parts := strings.Split(elp.email, "@")
	if len(parts) != 2 {
		return fmt.Errorf("invalid email format: %s", elp.email)
	}
	domain := strings.ToLower(parts[1])
	var host string
	var port int
	var tls bool

	if provider, ok := imap.CommonProviders[domain]; ok {
		host = provider.Host
		port = provider.Port
		tls = provider.TLS
	} else {
		// Default IMAP settings
		host = fmt.Sprintf("imap.%s", domain)
		port = 993
		tls = true
	}

	account := &EmailAccount{
		UserMXID:         elp.user.MXID.String(),
		Email:            elp.email,
		Username:         elp.username,
		Password:         elp.password,
		Host:             host,
		Port:             port,
		TLS:              tls,
		CreatedAt:        time.Now(),
		LastSyncTime:     time.Now(),
		MonitoredFolders: elp.selectedNames,
	}

	logger.Debug().Str("host", host).Int("port", port).Bool("tls", tls).Msg("Attempting to save account to database")

	if err := elp.connector.DB.UpsertAccount(ctx, account); err != nil {
		logger.Error().Err(err).Msg("Failed to save account credentials to database")
		return err
	}

	logger.Info().Msg("Successfully saved account credentials to database")
	return nil
}

// ProviderInfo contains information about an email provider
type ProviderInfo struct {
	Name        string
	Domain      string
	NeedsAppPwd bool
	HelpURL     string
}

// detectEmailProvider detects the email provider from the email address
func (elp *EmailLoginProcess) detectEmailProvider() *ProviderInfo {
	if elp.email == "" {
		return nil
	}

	parts := strings.Split(elp.email, "@")
	if len(parts) != 2 {
		return nil
	}

	domain := strings.ToLower(parts[1])

	switch domain {
	case "gmail.com", "googlemail.com":
		return &ProviderInfo{
			Name:        "Gmail",
			Domain:      domain,
			NeedsAppPwd: true,
			HelpURL:     "https://support.google.com/accounts/answer/185833",
		}
	case "yahoo.com", "yahoo.co.uk", "yahoo.fr", "yahoo.de":
		return &ProviderInfo{
			Name:        "Yahoo",
			Domain:      domain,
			NeedsAppPwd: true,
			HelpURL:     "https://help.yahoo.com/kb/generate-third-party-passwords-sln15241.html",
		}
	case "outlook.com", "hotmail.com", "live.com", "msn.com":
		return &ProviderInfo{
			Name:        "Outlook/Hotmail",
			Domain:      domain,
			NeedsAppPwd: true,
			HelpURL:     "https://support.microsoft.com/en-us/account-billing/using-app-passwords-with-apps-that-don-t-support-two-step-verification-5896ed9b-4263-e681-128a-a6f2979a7944",
		}
	case "icloud.com", "me.com", "mac.com":
		return &ProviderInfo{
			Name:        "iCloud",
			Domain:      domain,
			NeedsAppPwd: true,
			HelpURL:     "https://support.apple.com/en-us/HT204397",
		}
	default:
		return &ProviderInfo{
			Name:        "Custom Provider",
			Domain:      domain,
			NeedsAppPwd: false,
			HelpURL:     "",
		}
	}
}

// buildConnectionErrorMessage creates a helpful error message based on the provider
func (elp *EmailLoginProcess) buildConnectionErrorMessage(err error, provider *ProviderInfo) string {
	baseError := fmt.Sprintf("Connection failed: %v", err)

	if provider == nil {
		return baseError
	}

	// Check for common authentication errors
	errorStr := strings.ToLower(err.Error())
	isAuthError := strings.Contains(errorStr, "authentication") ||
		strings.Contains(errorStr, "login") ||
		strings.Contains(errorStr, "password") ||
		strings.Contains(errorStr, "credentials")

	if isAuthError && provider.NeedsAppPwd {
		return fmt.Sprintf(`❌ **%s Login Failed**

**Most likely cause:** You need to use an **App Password** instead of your regular password.

**How to fix:**
1. Go to your %s security settings
2. Enable 2-Factor Authentication (if not already enabled)
3. Generate an App Password specifically for this email bridge
4. Use the App Password instead of your regular password

**Help Link:** %s

**Original Error:** %v`,
			provider.Name, provider.Name, provider.HelpURL, err)
	}

	if isAuthError {
		return fmt.Sprintf(`❌ **%s Login Failed**

**Possible causes:**
• Incorrect email address or password
• Two-factor authentication enabled (you may need an App Password)
• IMAP access disabled in your email settings
• Account temporarily locked

**Please double-check:**
✓ Email address is correct
✓ Password is correct (or use App Password if 2FA is enabled)
✓ IMAP is enabled in your %s settings

**Original Error:** %v`,
			provider.Name, provider.Name, err)
	}

	// Handle custom providers (auto-detection fallback) differently
	if provider.Name == "Custom Provider" {
		parts := strings.Split(elp.email, "@")
		domain := "your email provider"
		fallbackHost := "imap.domain.com"
		if len(parts) == 2 {
			domain = parts[1]
			fallbackHost = fmt.Sprintf("imap.%s", domain)
		}

		return fmt.Sprintf(`❌ **Connection Failed - Auto-Detection Used**

🔍 **We attempted to connect using:** %s:993

**This is an unknown email provider, so we used auto-detection.**

**Possible solutions:**

**1. Check with %s for correct IMAP settings:**
• IMAP server address (might not be %s)
• Port number (usually 993 or 143)
• Security settings (SSL/TLS)
• IMAP access needs to be enabled

**2. Common IMAP settings to try:**
• mail.%s:993 (SSL)
• %s:993 (SSL)  
• %s:143 (STARTTLS)

**3. If your provider uses non-standard settings:**
Contact your email administrator or check your provider's documentation

**Original Error:** %v`,
			fallbackHost, domain, fallbackHost, domain, fallbackHost, fallbackHost, err)
	}

	// Generic connection error for known providers
	return fmt.Sprintf(`❌ **Connection to %s Failed**

**Possible causes:**
• Network connectivity issues
• Firewall blocking IMAP connections
• Email provider server temporarily unavailable
• Account settings may need adjustment

**Please try:**
✓ Check your internet connection
✓ Verify IMAP is enabled in your %s account settings
✓ Try again in a few minutes
✓ Contact your email provider if the issue persists

**Original Error:** %v`,
		provider.Name, provider.Name, err)
}

// IMAP monitoring is now handled by the EmailClient in client.go

// ----------------------------------------------------------------------------
// OAuth (Gmail) login flow — Phase B
// ----------------------------------------------------------------------------

// handleOAuthEmail captures the user's email, kicks off Google's device-code
// dance, and returns the URL+code for the user to enter in their browser.
// A background goroutine polls the token endpoint; the next user submission
// (any input) advances to handleOAuthWait.
func (elp *EmailLoginProcess) handleOAuthEmail(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	for k, v := range input {
		if k == "email" {
			elp.email = strings.TrimSpace(v)
		}
	}
	if elp.email == "" {
		return nil, fmt.Errorf("email address is required")
	}
	if elp.username == "" {
		elp.username = elp.email
	}

	cfg := elp.connector.Config.GmailOAuth
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("gmail_oauth.client_id / client_secret not configured on this bridge — ask your admin to set up Gmail OAuth, or use the email-password flow")
	}

	// Pre-create the account row so SaveOAuthToken (which UPDATEs in place)
	// finds something to write into. Persist provider info early so the
	// account is recoverable even if the bridge restarts mid-poll.
	parts := strings.Split(elp.email, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid email format: %s", elp.email)
	}
	domain := strings.ToLower(parts[1])
	host := fmt.Sprintf("imap.%s", domain)
	port := 993
	tls := true
	if provider, ok := imap.CommonProviders[domain]; ok {
		host = provider.Host
		port = provider.Port
		tls = provider.TLS
	}
	account := &EmailAccount{
		UserMXID:         elp.user.MXID.String(),
		Email:            elp.email,
		Username:         elp.username,
		Password:         "", // unused under OAuth; encryptString accepts empty
		Host:             host,
		Port:             port,
		TLS:              tls,
		CreatedAt:        time.Now(),
		LastSyncTime:     time.Now(),
		MonitoredFolders: []string{"INBOX"},
	}
	if err := elp.connector.DB.UpsertAccount(ctx, account); err != nil {
		return nil, fmt.Errorf("failed to pre-create account row: %w", err)
	}

	// Kick off the device-code dance using a long-lived (10min) context that
	// outlives the synchronous bridgev2 step call.
	emailCfg := email.GmailOAuthConfig{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret}
	dcCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	dcr, err := email.DeviceCodeStart(dcCtx, emailCfg)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start OAuth device flow: %w", err)
	}
	elp.dcr = dcr
	elp.oauthCtx = dcCtx
	elp.oauthCancel = cancel

	// Poll asynchronously so the user can hit "I authorized" without us
	// blocking the Matrix step UI for minutes.
	go elp.runOAuthPoll(emailCfg)

	elp.currentStep = "oauth_wait"
	return &bridgev2.LoginStep{
		Type:   bridgev2.LoginStepTypeUserInput,
		StepID: "oauth_wait",
		Instructions: fmt.Sprintf(`🔐 **Step 2 of 3 — Authorize matrimail with Google**

1. Open this URL in your browser: **%s**
2. Sign in to **%s** if prompted.
3. Enter this code: **%s**
4. Authorize matrimail.

This code expires in %d seconds.

When you're done, send any message here (e.g. ` + "`done`" + `) and the bridge will check for authorization. If Google reports "still pending", just send another message — we'll keep polling for you.`,
			dcr.VerificationURL, elp.email, dcr.UserCode, dcr.ExpiresIn),
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:        bridgev2.LoginInputFieldTypeUsername,
				ID:          "ack",
				Name:        "Acknowledge",
				Description: "Type 'done' once you've authorized in the browser",
			}},
		},
	}, nil
}

// runOAuthPoll runs in a goroutine spawned from handleOAuthEmail. It waits for
// the user to authorize (or for the device code to expire) and stores the
// resulting token (or error) on the login process.
func (elp *EmailLoginProcess) runOAuthPoll(cfg email.GmailOAuthConfig) {
	tok, err := email.DeviceCodePoll(elp.oauthCtx, cfg, elp.dcr)
	elp.pollMu.Lock()
	defer elp.pollMu.Unlock()
	elp.pollDone = true
	elp.pollToken = tok
	elp.pollErr = err
}

// handleOAuthWait is invoked when the user pings back after authorizing in
// the browser. If the poll hasn't completed yet, re-prompt; if it succeeded,
// validate via Gmail's userinfo, persist the token, and continue into folder
// selection.
func (elp *EmailLoginProcess) handleOAuthWait(ctx context.Context, _ map[string]string) (*bridgev2.LoginStep, error) {
	elp.pollMu.Lock()
	done := elp.pollDone
	tok := elp.pollToken
	pErr := elp.pollErr
	elp.pollMu.Unlock()

	if !done {
		return &bridgev2.LoginStep{
			Type:   bridgev2.LoginStepTypeUserInput,
			StepID: "oauth_wait",
			Instructions: fmt.Sprintf(`⏳ Still waiting for you to authorize at **%s** (code **%s**).

Once you've signed in and clicked "Allow", reply here again and I'll check.`, elp.dcr.VerificationURL, elp.dcr.UserCode),
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{{
					Type:        bridgev2.LoginInputFieldTypeUsername,
					ID:          "ack",
					Name:        "Acknowledge",
					Description: "Reply once you've authorized",
				}},
			},
		}, nil
	}

	// Stop the poll context regardless of outcome.
	if elp.oauthCancel != nil {
		elp.oauthCancel()
	}

	if pErr != nil {
		return nil, fmt.Errorf("OAuth authorization failed: %w", pErr)
	}
	if tok == nil {
		return nil, errors.New("OAuth poll returned no token")
	}

	// Verify the authorized identity matches what the user typed by hitting
	// users.getProfile (cheap, no further scopes needed under
	// https://mail.google.com/).
	emailCfg := email.GmailOAuthConfig{
		ClientID:     elp.connector.Config.GmailOAuth.ClientID,
		ClientSecret: elp.connector.Config.GmailOAuth.ClientSecret,
	}
	ts := email.TokenSource(ctx, emailCfg, tok)
	svc, err := gmail.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("create gmail service: %w", err)
	}
	prof, err := svc.Users.GetProfile("me").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("verify gmail profile: %w", err)
	}
	if !strings.EqualFold(prof.EmailAddress, elp.email) {
		return nil, fmt.Errorf("authorized account %s does not match the email you entered (%s); please restart login", prof.EmailAddress, elp.email)
	}

	// Persist token under the existing account row.
	if err := elp.connector.DB.SaveOAuthToken(ctx, elp.user.MXID.String(), elp.email,
		OAuthProviderGoogle, tok); err != nil {
		return nil, fmt.Errorf("save oauth token: %w", err)
	}

	// Provider name for downstream prompts.
	if pi := elp.detectEmailProvider(); pi != nil {
		elp.providerName = pi.Name
	} else {
		elp.providerName = "Gmail"
	}

	// List folders via IMAP using the access token (XOAUTH2).
	folders, err := elp.listFoldersOAuth(ctx, tok.AccessToken)
	if err != nil {
		// Fall back to INBOX-only — we still have a usable account; folder
		// selection just becomes implicit.
		elp.connector.Bridge.Log.Warn().Err(err).Msg("OAuth IMAP folder listing failed, falling back to INBOX only")
		elp.selectedNames = []string{"INBOX"}
		elp.selectedFolders = []imap.FolderInfo{{
			Name: "INBOX", Display: "INBOX", Icon: "📥", Type: imap.FolderTypeStandard,
		}}
		return elp.completeLogin(ctx)
	}
	elp.availableFolders = folders
	if len(folders) == 0 {
		elp.selectedNames = []string{"INBOX"}
		elp.selectedFolders = []imap.FolderInfo{{
			Name: "INBOX", Display: "INBOX", Icon: "📥", Type: imap.FolderTypeStandard,
		}}
		return elp.completeLogin(ctx)
	}
	elp.currentStep = "folder_selection"
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "folder_selection",
		Instructions: BuildFolderSelectionPrompt(folders, elp.providerName),
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:        bridgev2.LoginInputFieldTypeUsername,
				ID:          "folder_selection",
				Name:        "Folder Selection",
				Description: "Enter folder number(s), 'default' for INBOX, or 'cancel'",
			}},
		},
	}, nil
}

// listFoldersOAuth opens a one-shot IMAP connection authenticated via XOAUTH2
// for folder enumeration. Uses imap.NewClientOAuth to keep the auth path
// localized to one place.
func (elp *EmailLoginProcess) listFoldersOAuth(ctx context.Context, accessToken string) ([]imap.FolderInfo, error) {
	_ = ctx // Connect uses its own internal timeouts
	logger := elp.connector.Bridge.Log.With().
		Str("component", "login_test_oauth").
		Str("email", elp.email).
		Logger()

	client, err := imap.NewClientOAuth(elp.email, elp.email, accessToken, nil, &logger,
		elp.connector.Config.Logging.Sanitized,
		elp.connector.Config.Logging.PseudonymSecret,
		elp.connector.Config.Network.IMAP.StartupBackfillSeconds,
		elp.connector.Config.Network.IMAP.StartupBackfillMax,
		elp.connector.Config.Network.IMAP.InitialIdleTimeoutSeconds,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("create oauth IMAP client: %w", err)
	}
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("oauth IMAP connect: %w", err)
	}
	defer client.Disconnect()
	return client.ListFolders()
}
