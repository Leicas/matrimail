package connector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Leicas/matrimail/pkg/imap"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
)

var (
	HelpSectionAuth  = commands.HelpSection{Name: "Authentication", Order: 10}
	HelpSectionInfo  = commands.HelpSection{Name: "Information", Order: 5}
	HelpSectionAdmin = commands.HelpSection{Name: "Administration", Order: 15}
)

func fnPing(ce *commands.Event) {
	ce.Reply("🏓 **Pong!** Matrimail is alive and running.")
}

func fnStatus(ce *commands.Event, connector *EmailConnector) {
	logins := ce.User.GetUserLogins()

	if len(logins) == 0 {
		ce.Reply(`
**Matrimail Status**

**Connection Status:** Not connected
**Email Accounts:** 0
**Matrix Rooms:** 0 email rooms

**Bridge is ready!** Use ` + "`!matrimail login`" + ` to connect your first email account.
`)
		return
	}

	// Get real account status from IMAP manager
	if connector == nil || connector.IMAPManager == nil {
		ce.Reply("⚠️ **Bridge Error:** IMAP manager not initialized")
		return
	}

	accountStatuses := connector.IMAPManager.GetAccountStatus(ce.User.MXID.String())

	if len(accountStatuses) == 0 {
		ce.Reply(`
**Matrimail Status**

**Connection Status:** No active email connections
**Email Accounts:** 0 monitoring
**Matrix Rooms:** 0 email rooms

**Note:** You have bridge login(s) but no active IMAP connections. Use ` + "`!matrimail login`" + ` to add email accounts.
`)
		return
	}

	// Build status report
	statusMsg := `
**Matrimail Status**

`

	connectedCount := 0
	idleCount := 0

	for _, status := range accountStatuses {
		if status.Connected {
			connectedCount++
		}
		if status.IDLEActive {
			idleCount++
		}

		var statusIcon string
		var statusText string

		if status.Connected && status.IDLEActive {
			statusIcon = "✅"
			statusText = "Connected, monitoring"
		} else if status.Connected {
			statusIcon = "🔄"
			statusText = "Connected, starting monitoring"
		} else {
			statusIcon = "❌"
			statusText = "Disconnected"
		}

		statusMsg += fmt.Sprintf("📧 %s **%s** (%s:%d) - %s\n", statusIcon, status.Email, status.Host, status.Port, statusText)
	}

	statusMsg += fmt.Sprintf(`
**Summary:**
**Email Accounts:** %d total, %d connected
**Real-time Monitoring:** %d active IMAP IDLE sessions
**Matrix Rooms:** Calculating...

`, len(accountStatuses), connectedCount, idleCount)

	if connectedCount == len(accountStatuses) && idleCount == len(accountStatuses) {
		statusMsg += "✅ **All systems operational!** Your emails are being monitored in real-time."
	} else if connectedCount > 0 {
		statusMsg += "⚠️ **Partial connectivity** - Some accounts may need attention."
	} else {
		statusMsg += "❌ **No active connections** - Use `!matrimail login` to reconnect."
	}

	ce.Reply(statusMsg)
}

// fnNuke deletes the bridge database files immediately.
// This is intended to be called by the homeserver/bridge bot during bridge removal.
func fnNuke(ce *commands.Event, connector *EmailConnector) {
	// Require explicit confirmation to avoid accidental data loss
	if len(ce.Args) == 0 || strings.ToLower(ce.Args[0]) != "confirm" {
		ce.Reply("⚠️ This will DELETE the bridge database files and cannot be undone.\nConfirm with: `!matrimail nuke confirm`.")
		return
	}
	if connector == nil {
		ce.Reply("⚠️ Bridge not initialized.")
		return
	}
	// Stop IMAP to release DB handles
	if connector.IMAPManager != nil {
		connector.IMAPManager.StopAll()
	}

	// Get current working directory for secure path resolution
	cwd, err := os.Getwd()
	if err != nil {
		ce.Reply("❌ Failed to get current directory: %s", err.Error())
		return
	}

	// Try common DB file locations using absolute paths for security.
	// Includes legacy emaildawg.db* names so users migrating from the old
	// binary can still nuke cleanly.
	relativeCandidates := []string{
		"matrimail.db",
		"matrimail.db-wal",
		"matrimail.db-shm",
		"data/matrimail.db",
		"data/matrimail.db-wal",
		"data/matrimail.db-shm",
		"sh-matrimail.db",
		"sh-matrimail.db-wal",
		"sh-matrimail.db-shm",
		"emaildawg.db",
		"emaildawg.db-wal",
		"emaildawg.db-shm",
		"data/emaildawg.db",
		"data/emaildawg.db-wal",
		"data/emaildawg.db-shm",
		"sh-emaildawg.db",
		"sh-emaildawg.db-wal",
		"sh-emaildawg.db-shm",
	}

	var candidates []string
	for _, relPath := range relativeCandidates {
		absPath := filepath.Join(cwd, relPath)
		// Validate the path is within the working directory for security
		if cleanPath := filepath.Clean(absPath); strings.HasPrefix(cleanPath, cwd) {
			candidates = append(candidates, cleanPath)
		}
	}

	removed := 0
	for _, path := range candidates {
		if err := os.Remove(path); err == nil {
			removed++
		}
	}
	if removed == 0 {
		ce.Reply("ℹ️ No bridge DB files found to delete.")
		return
	}
	ce.Reply("🧨 Bridge database deleted (%d file(s)). Please restart the bridge service.", removed)
}

func fnLogin(ce *commands.Event, connector *EmailConnector) {
	// Check if user has any active logins
	logins := ce.User.GetUserLogins()
	if len(logins) > 0 {
		ce.Reply("✅ You're already logged into %d email account(s). Use `!matrimail list` to see them, or `!matrimail logout` to disconnect.", len(logins))
		return
	}

	ctx := context.Background()

	// Check if the user provided arguments in the command
	args := strings.TrimSpace(ce.RawArgs)
	if args != "" {
		// Parse text arguments: email:user@domain.com password:pass or password:"quoted pass"
		email, password, err := parseLoginArgs(args)
		if err != nil {
			ce.Reply("❌ %s\n\n**Usage:** `!matrimail login email:your@email.com password:yourpassword`\n**Or:** `!matrimail login email:your@email.com password:\"password with spaces\"`", err.Error())
			return
		}

		// Process the text-based login
		err = processTextLogin(ctx, ce, email, password, connector)
		if err != nil {
			ce.Reply("❌ Login failed: %s", err.Error())
		}
		return
	}

	// Fallback to interactive login process using bridgev2 forms.
	// If Gmail OAuth is configured at the bridge level, offer a choice;
	// otherwise stay on the historical app-password flow.
	flowID := pickInteractiveLoginFlow(connector)
	connector.Bridge.Log.Info().Str("flow_id", flowID).Msg("matrimail: starting interactive login flow")

	loginProcess, err := connector.CreateLogin(ctx, ce.User, flowID)
	if err != nil {
		ce.Reply("❌ Failed to start login process: %s", err.Error())
		return
	}

	// Start the login flow
	step, err := loginProcess.Start(ctx)
	if err != nil {
		ce.Reply("❌ Failed to start login: %s", err.Error())
		return
	}

	// Send the login instructions to the user. Only append the app-password
	// help block when we're actually on the password flow — for OAuth, the
	// step's own Instructions ARE the right prompt (URL + user code).
	if flowID == LoginFlowIDPassword {
		ce.Reply(buildEnhancedLoginInstructions(step.Instructions))
	} else {
		ce.Reply(step.Instructions)
	}
}

// pickInteractiveLoginFlow returns LoginFlowIDOAuthGmail when the bridge is
// configured for Gmail OAuth, otherwise LoginFlowIDPassword. The bot command
// historically hardcoded the password flow, which left OAuth-configured
// bridges without a way to start the OAuth dance from a text command.
//
// Heuristic: prefer OAuth iff the admin has wired up gmail_oauth credentials.
// We can't sniff the user's email here (the form will collect that next),
// so this is a bridge-wide default; users on non-Gmail domains will be
// gracefully steered back to the password flow by handleOAuthEmail's email
// validation if they end up at the OAuth prompt by mistake.
func pickInteractiveLoginFlow(connector *EmailConnector) string {
	cfg := connector.Config.GmailOAuth
	if cfg.ClientID != "" && cfg.ClientSecret != "" {
		return LoginFlowIDOAuthGmail
	}
	return LoginFlowIDPassword
}

func fnLogout(ce *commands.Event, connector *EmailConnector) {
	_ = connector // parameter kept for API consistency
	logins := ce.User.GetUserLogins()
	if len(logins) == 0 {
		ce.Reply("ℹ️ You're not connected to any email accounts. Use `!matrimail login` to get started.")
		return
	}

	// Check if user specified an email to logout from
	if len(ce.Args) > 0 {
		emailAddr := ce.Args[0]
		ce.Reply("🔌 Disconnecting from **%s**...", emailAddr)

		// Find the specific login for this email
		var targetLogin *bridgev2.UserLogin
		for _, login := range logins {
			if client, ok := login.Client.(*EmailClient); ok && client.Email == emailAddr {
				targetLogin = login
				break
			}
		}

		if targetLogin == nil {
			ce.Reply("❌ Email account **%s** not found in your connected accounts.", emailAddr)
			return
		}

		// Use LogoutRemote for proper cleanup
		ctx := context.Background()
		if client, ok := targetLogin.Client.(*EmailClient); ok {
			client.LogoutRemote(ctx)
			ce.Reply("✅ Successfully disconnected from **%s**", emailAddr)
		} else {
			ce.Reply("❌ Failed to disconnect from **%s**: invalid client type", emailAddr)
		}
		return
	}

	// Logout all accounts
	ce.Reply("🔌 Disconnecting from all %d email account(s)...", len(logins))

	// Use LogoutRemote for each login for proper cleanup
	ctx := context.Background()
	var failures []string

	for _, login := range logins {
		if client, ok := login.Client.(*EmailClient); ok {
			try := func() {
				client.LogoutRemote(ctx)
			}

			// Use a simple panic recovery to catch any logout failures
			func() {
				email := client.Email // Capture to avoid future capture pitfalls
				defer func() {
					if msg := recover(); msg != nil {
						failures = append(failures, fmt.Sprintf("LogoutRemote for %s: panic %v (%T)", email, msg, msg))
					}
				}()
				try()
			}()
		} else {
			failures = append(failures, fmt.Sprintf("Invalid client type for login %s", login.ID))
		}
	}

	if len(failures) == 0 {
		ce.Reply("✅ Successfully disconnected from all email accounts.")
	} else {
		ce.Reply("⚠️ Logout completed with some issues:\n• %s", strings.Join(failures, "\n• "))
	}
}

func fnList(ce *commands.Event, connector *EmailConnector) {
	// Check if user has any active logins
	logins := ce.User.GetUserLogins()
	if len(logins) == 0 {
		ce.Reply(`
📭 **No email accounts connected**

To get started:
1. Use ` + "`!matrimail login`" + ` to connect your first email account
2. The bridge supports Gmail, Outlook, Yahoo, FastMail, and custom IMAP servers
3. Once connected, new emails will automatically create Matrix rooms

Need help? Use ` + "`!matrimail help`" + ` for more information.
`)
		return
	}

	// Get real account list from database (without passwords for performance)
	ctx := context.Background()
	accounts, err := connector.DB.GetUserAccountsBasic(ctx, ce.User.MXID.String())
	if err != nil {
		ce.Reply("❌ Failed to get account list: %s", err.Error())
		return
	}

	if len(accounts) == 0 {
		ce.Reply("📭 No email accounts found in database. Use `!matrimail login` to add one.")
		return
	}

	// Get account status from IMAP manager
	statusMap := make(map[string]imap.AccountStatus)
	statuses := connector.IMAPManager.GetAccountStatus(ce.User.MXID.String())
	for _, status := range statuses {
		statusMap[status.Email] = status
	}

	// Build response
	response := fmt.Sprintf("📧 **Connected Email Accounts:** %d\n\n", len(accounts))

	for _, account := range accounts {
		status, hasStatus := statusMap[account.Email]

		var statusIcon string
		var statusText string
		var provider string

		// Determine provider name
		domain := strings.ToLower(strings.Split(account.Email, "@")[1])
		if p, ok := imap.CommonProviders[domain]; ok {
			provider = p.Name
		} else {
			provider = "Custom IMAP"
		}

		if hasStatus {
			if status.Connected && status.IDLEActive {
				statusIcon = "✅"
				statusText = "Connected, monitoring"
			} else if status.Connected {
				statusIcon = "🔄"
				statusText = "Connected, starting monitoring"
			} else {
				statusIcon = "❌"
				statusText = "Disconnected"
			}
		} else {
			statusIcon = "⚠️"
			statusText = "Status unknown"
		}

		response += fmt.Sprintf("• %s **%s** (%s) - %s\n", statusIcon, account.Email, provider, statusText)
		if hasStatus && status.Host != "" {
			response += fmt.Sprintf("    Server: %s:%d\n", status.Host, status.Port)
		}
		// Show monitored folders
		if len(account.MonitoredFolders) > 0 {
			response += fmt.Sprintf("    📁 Folders: %s\n", strings.Join(account.MonitoredFolders, ", "))
		}
		response += fmt.Sprintf("    Added: %s\n\n", account.CreatedAt.Format("Jan 2, 2006"))
	}

	response += "💡 Use `!matrimail logout <email>` to remove a specific account."
	ce.Reply(response)
}

func fnSync(ce *commands.Event, connector *EmailConnector) {
	_ = connector // parameter kept for API consistency
	logins := ce.User.GetUserLogins()
	if len(logins) == 0 {
		ce.Reply("ℹ️ You're not connected to any email accounts. Use `!matrimail login` to get started.")
		return
	}

	// If specific account provided, sync only that one
	if len(ce.Args) > 0 {
		emailAddr := ce.Args[0]
		var targetLogin *bridgev2.UserLogin
		for _, login := range logins {
			if client, ok := login.Client.(*EmailClient); ok && client.Email == emailAddr {
				targetLogin = login
				break
			}
		}

		if targetLogin == nil {
			ce.Reply("❌ Email account **%s** not found in your connected accounts.", emailAddr)
			return
		}

		ce.Reply("🔄 Forcing sync for **%s**...", emailAddr)
		if client, ok := targetLogin.Client.(*EmailClient); ok {
			if client.IMAPClient != nil && client.IMAPClient.IsConnected() {
				if err := client.IMAPClient.CheckNewMessages(); err != nil {
					ce.Reply("❌ Sync failed for **%s**: %s", emailAddr, err.Error())
					return
				}
				ce.Reply("✅ Sync completed for **%s**", emailAddr)
			} else {
				ce.Reply("⚠️ **%s** is not connected to IMAP server", emailAddr)
			}
		}
		return
	}

	// Sync all accounts
	ce.Reply("🔄 Forcing sync for all %d email account(s)...", len(logins))
	var successes []string
	var failures []string

	// Create context with timeout for all sync operations, derived from command context
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, login := range logins {
		if client, ok := login.Client.(*EmailClient); ok {
			// Capture IMAP client reference safely to prevent nil pointer panic
			imapClient := client.IMAPClient
			if imapClient != nil && imapClient.IsConnected() {
				// Create individual context for each sync operation
				syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
				done := make(chan error, 1)

				go func() {
					defer syncCancel() // Ensure context is cancelled when goroutine exits
					select {
					case done <- imapClient.CheckNewMessages():
					case <-syncCtx.Done():
						// Context cancelled, exit goroutine cleanly
						return
					}
				}()

				select {
				case err := <-done:
					syncCancel() // Cancel context as operation completed
					if err != nil {
						failures = append(failures, fmt.Sprintf("%s: %s", client.Email, err.Error()))
					} else {
						successes = append(successes, client.Email)
					}
				case <-syncCtx.Done():
					// Timeout occurred, context will cancel the goroutine
					failures = append(failures, fmt.Sprintf("%s: sync timed out after 30 seconds", client.Email))
				}
			} else {
				failures = append(failures, fmt.Sprintf("%s: not connected", client.Email))
			}
		}
	}

	var result strings.Builder
	if len(successes) > 0 {
		result.WriteString(fmt.Sprintf("✅ Successfully synced: %s\n", strings.Join(successes, ", ")))
	}
	if len(failures) > 0 {
		result.WriteString(fmt.Sprintf("❌ Failed to sync: %s", strings.Join(failures, "; ")))
	}

	ce.Reply(result.String())
}

func fnReconnect(ce *commands.Event, connector *EmailConnector) {
	_ = connector // parameter kept for API consistency
	logins := ce.User.GetUserLogins()
	if len(logins) == 0 {
		ce.Reply("ℹ️ You're not connected to any email accounts. Use `!matrimail login` to get started.")
		return
	}

	// If specific account provided, reconnect only that one
	if len(ce.Args) > 0 {
		emailAddr := ce.Args[0]
		var targetLogin *bridgev2.UserLogin
		for _, login := range logins {
			if client, ok := login.Client.(*EmailClient); ok && client.Email == emailAddr {
				targetLogin = login
				break
			}
		}

		if targetLogin == nil {
			ce.Reply("❌ Email account **%s** not found in your connected accounts.", emailAddr)
			return
		}

		ce.Reply("🔌 Reconnecting **%s**...", emailAddr)
		if client, ok := targetLogin.Client.(*EmailClient); ok {
			// Capture IMAP client reference safely to prevent nil pointer panic
			imapClient := client.IMAPClient
			if imapClient != nil {
				if err := imapClient.Reconnect(); err != nil {
					ce.Reply("❌ Reconnection failed for **%s**: %s", emailAddr, err.Error())
					return
				}
				// Start IDLE after successful reconnect
				if err := imapClient.StartIDLE(); err != nil {
					ce.Reply("⚠️ Reconnected **%s** but IDLE failed to start: %s", emailAddr, err.Error())
				} else {
					ce.Reply("✅ Successfully reconnected **%s**", emailAddr)
				}
			} else {
				ce.Reply("❌ No IMAP client found for **%s**", emailAddr)
			}
		}
		return
	}

	// Reconnect all accounts
	ce.Reply("🔌 Reconnecting all %d email account(s)...", len(logins))
	var successes []string
	var failures []string

	for _, login := range logins {
		if client, ok := login.Client.(*EmailClient); ok {
			// Capture IMAP client reference safely to prevent nil pointer panic
			imapClient := client.IMAPClient
			if imapClient != nil {
				if err := imapClient.Reconnect(); err != nil {
					failures = append(failures, fmt.Sprintf("%s: %s", client.Email, err.Error()))
					continue
				}
				if err := imapClient.StartIDLE(); err != nil {
					failures = append(failures, fmt.Sprintf("%s: IDLE failed - %s", client.Email, err.Error()))
				} else {
					successes = append(successes, client.Email)
				}
			}
		}
	}

	var result strings.Builder
	if len(successes) > 0 {
		result.WriteString(fmt.Sprintf("✅ Successfully reconnected: %s\n", strings.Join(successes, ", ")))
	}
	if len(failures) > 0 {
		result.WriteString(fmt.Sprintf("❌ Failed to reconnect: %s", strings.Join(failures, "; ")))
	}

	ce.Reply(result.String())
}

// parseLoginArgs parses command arguments in the format: email:user@domain.com password:pass or password:"quoted pass"
func parseLoginArgs(args string) (email, password string, err error) {
	// Split by spaces but preserve quoted strings
	parts := parseQuotedArgs(args)

	for _, part := range parts {
		if strings.HasPrefix(part, "email:") {
			email = strings.TrimPrefix(part, "email:")
		} else if strings.HasPrefix(part, "password:") {
			password = strings.TrimPrefix(part, "password:")
		}
	}

	if email == "" {
		return "", "", fmt.Errorf("email is required")
	}
	if password == "" {
		return "", "", fmt.Errorf("password is required")
	}

	// Validate email format
	if !strings.Contains(email, "@") || !strings.Contains(email, ".") {
		return "", "", fmt.Errorf("invalid email format")
	}

	return email, password, nil
}

// parseQuotedArgs splits arguments while preserving quoted strings
func parseQuotedArgs(args string) []string {
	var result []string
	var current strings.Builder
	inQuotes := false
	escaped := false

	for i, r := range args {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			inQuotes = !inQuotes
		case r == ' ' && !inQuotes:
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}

		// Handle end of string
		if i == len(args)-1 && current.Len() > 0 {
			result = append(result, current.String())
		}
	}

	return result
}

// processTextLogin processes a text-based login using the same flow as the interactive login
func processTextLogin(ctx context.Context, ce *commands.Event, email, password string, connector *EmailConnector) error {
	// Create a login process
	loginProcess, err := connector.CreateLogin(ctx, ce.User, "email-password")
	if err != nil {
		return fmt.Errorf("failed to create login process: %w", err)
	}

	// Cast to our EmailLoginProcess to access internal methods
	emailLogin, ok := loginProcess.(*EmailLoginProcess)
	if !ok {
		return fmt.Errorf("unexpected login process type")
	}

	// Validate inputs before setting (additional validation beyond parseLoginArgs)
	email = strings.TrimSpace(email)
	password = strings.TrimSpace(password)

	if len(email) > 256 {
		return fmt.Errorf("email address too long (max 256 characters)")
	}
	if len(password) > 1024 {
		return fmt.Errorf("password too long (max 1024 characters)")
	}
	if len(password) == 0 {
		return fmt.Errorf("password cannot be empty")
	}

	// Set the credentials directly
	emailLogin.email = email
	emailLogin.username = email
	emailLogin.password = password

	// Submit the credentials as if they came from form input
	inputData := map[string]string{
		"email":    email,
		"password": password,
	}

	step, err := emailLogin.SubmitUserInput(ctx, inputData)
	if err != nil {
		return err
	}

	// Send success message
	ce.Reply(step.Instructions)
	return nil
}

// buildEnhancedLoginInstructions enhances the form-based instructions with text command info
func buildEnhancedLoginInstructions(originalInstructions string) string {
	// Include original form-based instructions at the top if provided
	prefix := ""
	if strings.TrimSpace(originalInstructions) != "" {
		prefix = strings.TrimSpace(originalInstructions) + "\n\n"
	}
	return prefix + `🔐 **Email Bridge Login**

**Method 1: Quick Command**
` + "`!matrimail login email:your@email.com password:yourpassword`" + `
` + "`!matrimail login email:your@email.com password:\"password with spaces\"`" + `

**Method 2: Form Fields (if supported by your client)**
📧 **Please enter your email credentials using the form fields below.**

**Important Notes:**
• For Gmail/Yahoo/Outlook: Use an **App Password** (not your regular password)
• The bridge will automatically detect your email provider settings
• Your password will be encrypted and stored securely

**App Password Setup Guide:**
**Gmail:** Settings → Security → 2-Step Verification → App passwords
**Yahoo:** Account Info → Account security → Generate app password  
**Outlook:** Security → Sign-in options → App passwords
**iCloud:** Sign-In and Security → App-Specific Passwords

**Popular Providers Supported:**
✅ Gmail, Yahoo, Outlook, iCloud, FastMail - Auto-configured
✅ Custom IMAP servers - Auto-detected

*The bridge will test your IMAP connection automatically after you submit your credentials.*

**Need help?** Use ` + "`!matrimail help`" + ` for more information or ` + "`!matrimail status`" + ` to check connection status.`
}

func fnPassphrase(ce *commands.Event, connector *EmailConnector) {
	_ = connector // parameter kept for API consistency
	if len(ce.Args) == 0 {
		// Show current status and usage
		passphrasePath, err := getPassphraseFilePath()
		if err != nil {
			ce.Reply("❌ Failed to get passphrase file path: %s", err.Error())
			return
		}

		// Check if passphrase file exists
		exists := false
		if _, err := os.Stat(passphrasePath); err == nil {
			exists = true
		}

		// Check if environment variable is set
		envSet := strings.TrimSpace(os.Getenv("MATRIMAIL_PASSPHRASE")) != ""

		ce.Reply(`🔐 **Encryption Passphrase Status**

**Environment Variable:** %s
**Passphrase File:** %s
**File Location:** %s

**Usage:**
• `+"`!matrimail passphrase generate`"+` - Generate new secure passphrase
• `+"`!matrimail passphrase show-location`"+` - Show passphrase file path  
• `+"`!matrimail passphrase set <passphrase>`"+` - Set custom passphrase

**Security Note:** Your email passwords are encrypted using this passphrase. Matrimail automatically generates one if neither environment variable nor file exists.`,
			map[bool]string{true: "✅ Set", false: "❌ Not set"}[envSet],
			map[bool]string{true: "✅ Exists", false: "❌ Not found"}[exists],
			passphrasePath)
		return
	}

	command := strings.ToLower(ce.Args[0])

	switch command {
	case "generate":
		// Generate new passphrase
		passphrase, err := generateAndStorePassphrase()
		if err != nil {
			ce.Reply("❌ Failed to generate passphrase: %s", err.Error())
			return
		}

		passphrasePath, _ := getPassphraseFilePath()
		ce.Reply(`✅ **New secure passphrase generated!**

**Passphrase:** `+"`%s`"+`
**Stored at:** %s
**Permissions:** 0600 (owner read/write only)

⚠️ **Important:** This passphrase encrypts your email passwords. Keep it secure!

**Next Steps:**
• Your existing email accounts will continue to work
• New logins will use this passphrase for encryption
• You can also set MATRIMAIL_PASSPHRASE environment variable for production use`,
			passphrase, passphrasePath)

	case "show-location":
		passphrasePath, err := getPassphraseFilePath()
		if err != nil {
			ce.Reply("❌ Failed to get passphrase file path: %s", err.Error())
			return
		}

		// Check if file exists
		exists := false
		if _, err := os.Stat(passphrasePath); err == nil {
			exists = true
		}

		ce.Reply(`📍 **Passphrase File Location**

**Path:** %s
**Status:** %s

**Platform-specific locations:**
• **Linux:** ~/.config/matrimail/passphrase
• **macOS:** ~/Library/Application Support/Matrimail/passphrase
• **Windows:** %%APPDATA%%\Roaming\Matrimail\passphrase

You can also set the MATRIMAIL_PASSPHRASE environment variable instead of using a file.`,
			passphrasePath,
			map[bool]string{true: "✅ File exists", false: "❌ File not found"}[exists])

	case "set":
		if len(ce.Args) < 2 {
			ce.Reply("❌ Missing passphrase argument.\n\n**Usage:** `!matrimail passphrase set <your-passphrase>`")
			return
		}

		// Join remaining args as the passphrase (in case it has spaces)
		passphrase := strings.Join(ce.Args[1:], " ")
		if len(passphrase) < 8 {
			ce.Reply("❌ Passphrase must be at least 8 characters long for security.")
			return
		}

		// Get passphrase file path
		passphrasePath, err := getPassphraseFilePath()
		if err != nil {
			ce.Reply("❌ Failed to get passphrase file path: %s", err.Error())
			return
		}

		// Create config directory with secure permissions
		configDir := filepath.Dir(passphrasePath)
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			ce.Reply("❌ Failed to create config directory: %s", err.Error())
			return
		}

		// Write passphrase file with secure permissions
		if err := os.WriteFile(passphrasePath, []byte(passphrase), 0o600); err != nil {
			ce.Reply("❌ Failed to write passphrase file: %s", err.Error())
			return
		}

		ce.Reply(`✅ **Custom passphrase set successfully!**

**Stored at:** %s
**Permissions:** 0600 (owner read/write only)

⚠️ **Important:** 
• This passphrase now encrypts your email passwords
• Existing email accounts will continue to work
• Make sure to remember this passphrase or store it securely
• You can override this by setting MATRIMAIL_PASSPHRASE environment variable`,
			passphrasePath)

	default:
		ce.Reply("❌ Unknown command: %s\n\n**Available commands:**\n• `generate` - Generate new secure passphrase\n• `show-location` - Show passphrase file location\n• `set <passphrase>` - Set custom passphrase", command)
	}
}

// fnCompose implements the `!matrimail compose` bot command. It picks a login
// (the user's first / default email account, or a `from:` override if we add
// one later), parses the to:/cc:/subject: arguments, and delegates to
// EmailClient.ResolveIdentifier with createChat=true. The resulting portal is
// materialized via Bridge.GetPortalByKey + Portal.CreateMatrixRoom — same path
// the framework's built-in `start-chat` command uses.
//
// The synthetic thread is updated in-place to attach the parsed Cc list and
// Subject so HandleMatrixMessage's draft branch can use them when the user
// types the first message.
func fnCompose(ce *commands.Event, connector *EmailConnector) {
	logins := ce.User.GetUserLogins()
	if len(logins) == 0 {
		ce.Reply("ℹ️ You're not connected to any email accounts. Use `!matrimail login` first.")
		return
	}

	args, err := parseComposeArgs(ce.RawArgs)
	if err != nil {
		ce.Reply("❌ %s\n\n**Usage:** `!matrimail compose to:alice@example.com [cc:bob@example.com] [subject:\"hello\"]`", err.Error())
		return
	}

	// Pick the first login's EmailClient. Multi-account users can disambiguate
	// in v2 with a `from:` argument; for now first-match is good enough and
	// matches how !matrimail logout / sync default.
	var client *EmailClient
	for _, login := range logins {
		if c, ok := login.Client.(*EmailClient); ok {
			client = c
			break
		}
	}
	if client == nil {
		ce.Reply("❌ Internal error: no usable EmailClient found among your logins.")
		return
	}

	resp, err := client.ResolveIdentifier(ce.Ctx, args.To, true)
	if err != nil {
		ce.Reply("❌ Failed to start compose thread: %s", err.Error())
		return
	}
	if resp == nil || resp.Chat == nil {
		ce.Reply("❌ Internal error: ResolveIdentifier returned no chat.")
		return
	}

	// Attach the parsed Cc + Subject to the synthetic thread so the first
	// HandleMatrixMessage call uses them. The thread was just inserted into
	// the cache by ResolveIdentifier; GetThreadByID returns the same pointer.
	threadID := strings.TrimPrefix(string(resp.Chat.PortalKey.ID), "thread:")
	if connector.ThreadManager != nil {
		if thread := connector.ThreadManager.GetThreadByID(string(client.UserLogin.ID), threadID); thread != nil {
			thread.Subject = args.Subject
			thread.Cc = append([]string(nil), args.Cc...)
			// Fold cc into Participants so resolveRecipients picks them up
			// alongside the to: address.
			for _, c := range args.Cc {
				thread.Participants = append(thread.Participants, strings.ToLower(c))
			}
			connector.ThreadManager.CacheForReceiver(string(client.UserLogin.ID), thread)
		}
	}

	portal, err := ce.Bridge.GetPortalByKey(ce.Ctx, resp.Chat.PortalKey)
	if err != nil {
		ce.Reply("❌ Failed to get portal: %s", err.Error())
		return
	}

	// Persist the draft to Portal.Metadata immediately so a bridge restart
	// before the first send doesn't lose the thread state.
	portal.Metadata = &PortalMetadata{
		ThreadID:     threadID,
		Subject:      args.Subject,
		Participants: append([]string{strings.ToLower(args.To)}, args.Cc...),
		IsDraft:      true,
	}

	chatInfo, err := client.GetChatInfo(ce.Ctx, portal)
	if err != nil {
		ce.Reply("❌ Failed to compute chat info: %s", err.Error())
		return
	}
	if portal.MXID == "" {
		if err := portal.CreateMatrixRoom(ce.Ctx, client.UserLogin, chatInfo); err != nil {
			ce.Reply("❌ Failed to create Matrix room: %s", err.Error())
			return
		}
	}
	// CreateMatrixRoom calls Save internally; if we skipped it (room already
	// existed for some reason), persist the metadata write explicitly.
	if portal.MXID != "" {
		if err := portal.Save(ce.Ctx); err != nil {
			connector.Bridge.Log.Warn().Err(err).Msg("compose: portal.Save (post-create) failed")
		}
	}

	subjLine := args.Subject
	if subjLine == "" {
		subjLine = "(no subject)"
	}
	ccLine := ""
	if len(args.Cc) > 0 {
		ccLine = fmt.Sprintf("\n**Cc:** %s", strings.Join(args.Cc, ", "))
	}
	ce.Reply("✉️ **New email thread ready**\n\n**To:** %s%s\n**Subject:** %s\n\nOpen the new room and type your message — it will be sent as the first email in the thread.\n\nRoom: [%s](%s)", args.To, ccLine, subjLine, portal.MXID, portal.MXID.URI().MatrixToURL())
}

// fnConfig handles bridge configuration commands
func fnConfig(ce *commands.Event, connector *EmailConnector) {
	if len(ce.Args) == 0 {
		ce.Reply(`⚙️ **Bridge Configuration**

**Available subcommands:**
• ` + "`!matrimail config folders`" + ` - Change which folders to monitor

**Current status:**
Use ` + "`!matrimail list`" + ` to see your connected accounts and their settings.`)
		return
	}

	subcommand := strings.ToLower(ce.Args[0])

	switch subcommand {
	case "folders":
		fnConfigFolders(ce, connector)
	default:
		ce.Reply("❌ Unknown config subcommand: `%s`\n\n**Available subcommands:**\n• `folders` - Change which folders to monitor", subcommand)
	}
}

// fnConfigFolders handles reconfiguring monitored folders for an account
// Requires re-authentication as per implementation plan
func fnConfigFolders(ce *commands.Event, connector *EmailConnector) {
	_ = connector // parameter kept for API consistency
	logins := ce.User.GetUserLogins()
	if len(logins) == 0 {
		ce.Reply("ℹ️ You're not connected to any email accounts. Use `!matrimail login` to get started.")
		return
	}

	// If multiple accounts, need to specify which one
	if len(logins) > 1 && len(ce.Args) < 2 {
		var accountList strings.Builder
		accountList.WriteString("📧 You have multiple email accounts. Please specify which account to reconfigure:\n\n")
		for _, login := range logins {
			if client, ok := login.Client.(*EmailClient); ok {
				accountList.WriteString(fmt.Sprintf("• `!matrimail config folders %s`\n", client.Email))
			}
		}
		ce.Reply(accountList.String())
		return
	}

	// Find the target account
	var targetEmail string
	if len(ce.Args) >= 2 {
		targetEmail = ce.Args[1]
	} else {
		// Single account - use it
		if client, ok := logins[0].Client.(*EmailClient); ok {
			targetEmail = client.Email
		}
	}

	// Verify the account exists
	var targetLogin *bridgev2.UserLogin
	for _, login := range logins {
		if client, ok := login.Client.(*EmailClient); ok && client.Email == targetEmail {
			targetLogin = login
			break
		}
	}

	if targetLogin == nil {
		ce.Reply("❌ Email account **%s** not found in your connected accounts.", targetEmail)
		return
	}

	ce.Reply(`🔐 **Folder Reconfiguration**

To change the monitored folders for **%s**, you'll need to re-authenticate.

**Why?** Re-authentication ensures your credentials are still valid and allows us to fetch the current folder list.

**To proceed:**
1. Use `+"`!matrimail logout %s`"+` to disconnect
2. Use `+"`!matrimail login`"+` to reconnect and choose new folders

💡 **Tip:** Your existing Matrix rooms will remain - only the folders being monitored will change.`, targetEmail, targetEmail)
}
