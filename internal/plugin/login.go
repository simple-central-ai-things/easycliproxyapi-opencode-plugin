// OAuth browser login for the opencode-go provider (Milestone: device-code
// flow). opencode.ai's console signs a member in with the OAuth 2.0 device
// authorization grant (RFC 8628): no PKCE and no loopback callback port, which
// is exactly what fits a headless plugin that cannot receive a browser redirect.
// The flow mirrors opencode's own console provider
// (packages/core/src/plugin/provider/opencode.ts): POST /auth/device/code with
// client_id "opencode-cli", show the user verification_uri_complete, poll
// POST /auth/device/token with grant_type device_code, then refresh later with
// grant_type refresh_token. All upstream traffic goes through the host http
// bridge — never an independent proxy stack.

package plugin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"opencode-go-cliproxyapi/internal/config"
)

// OAuth endpoints and constants, pinned from opencode's console provider. The
// console server and User-Agent default to the config package's values (single
// source of truth) so the login flow and the executor agree.
const (
	opencodeConsoleServer    = config.DefaultConsoleURL
	opencodeOAuthClientID    = "opencode-cli"
	opencodeDefaultUserAgent = config.DefaultUserAgent
	oauthDevicePath          = "/auth/device/code"
	oauthTokenPath           = "/auth/device/token"
	oauthUserPath            = "/api/user"
	oauthOrgsPath            = "/api/orgs"
	oauthManagedPrefix       = "opencode-go-oauth-"

	grantDeviceCode   = "urn:ietf:params:oauth:grant-type:device_code"
	grantRefreshToken = "refresh_token"

	// credential_type attribute values: one record is either an API key or an
	// OAuth token. The executor routes on the api_key attribute (the bearer,
	// which is the key OR the access token); refresh and parse route here.
	credentialTypeOAuth  = "oauth"
	credentialTypeAPIKey = "api_key"

	deviceErrorPending  = "authorization_pending"
	deviceErrorSlowDown = "slow_down"
	deviceErrorInvalid  = "invalid_grant"

	maxConcurrentLogins = 32

	// refreshSkew asks the host to refresh a token this long before it lapses,
	// so a request never lands with a token that has already expired.
	refreshSkew = 60 * time.Second
)

// deviceLogin is one in-flight device authorization attempt, keyed by the
// opaque state token handed back to the host. The device_code stays here and
// never crosses the wire as the state value.
type deviceLogin struct {
	server     string
	userAgent  string
	deviceCode string
	expiresAt  time.Time
}

// loginConfig resolves the console server and User-Agent for a login flow: the
// loaded config when present (registration has run), else the package defaults.
// Config may be zero-valued before the first register, so empty fields fall
// back to the constants.
func (m *Manager) loginConfig() (consoleURL, userAgent string) {
	m.mu.RLock()
	consoleURL = strings.TrimRight(m.cfg.ConsoleURL, "/")
	userAgent = strings.TrimSpace(m.cfg.UserAgent)
	m.mu.RUnlock()
	if consoleURL == "" {
		consoleURL = opencodeConsoleServer
	}
	if userAgent == "" {
		userAgent = opencodeDefaultUserAgent
	}
	return consoleURL, userAgent
}

// deviceCodeResp is the /auth/device/code answer.
type deviceCodeResp struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
}

// deviceTokenResp is the /auth/device/token answer for BOTH the device-code
// grant and the refresh-token grant; error is set on a rejected grant.
type deviceTokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	OrgID        string `json:"org_id"`
	Error        string `json:"error"`
}

// oauthRecord is the persisted credential for one console sign-in. The access
// token doubles as the bearer the executor sends (api_key attribute); the
// refresh token and server let the plugin renew it later.
type oauthRecord struct {
	Type           string `json:"type"`
	ID             string `json:"id"`
	Label          string `json:"label"`
	CredentialType string `json:"credential_type"`
	Server         string `json:"server"`
	ClientID       string `json:"client_id"`
	AccessToken    string `json:"access_token"`
	RefreshToken   string `json:"refresh_token"`
	ExpiresAt      int64  `json:"expires_at"` // unix seconds
	OrgID          string `json:"org_id,omitempty"`
	OrgName        string `json:"org_name,omitempty"`
	AccountID      string `json:"account_id,omitempty"`
	Email          string `json:"email,omitempty"`
}

func (p authProvider) loginManager() (*Manager, error) {
	if p.manager == nil || p.manager.bridge == nil {
		return nil, fmt.Errorf("opencode-go login requires a host http bridge")
	}
	return p.manager, nil
}

func jsonHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	return h
}

// randomToken returns n bytes of cryptographically random data, url-safe and
// unpadded. Used as the opaque login state.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("secure random generator unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// refreshAfter maps a unix-seconds expiry to the earliest refresh time, pulled
// in by refreshSkew and clamped to now so an already-expired token refreshes
// immediately.
func refreshAfter(expiresAt int64) time.Time {
	due := time.Unix(expiresAt, 0).UTC().Add(-refreshSkew)
	if due.Before(time.Now().UTC()) {
		return time.Now().UTC()
	}
	return due
}

// oauthRecordID derives the stable credential id from the most identifying,
// least volatile field available: the account id, then the email, then the
// refresh token. Re-login for the same account overwrites the same record.
func oauthRecordID(accountID, email, refreshToken string) string {
	key := strings.TrimSpace(accountID)
	if key == "" {
		key = strings.TrimSpace(email)
	}
	if key == "" {
		key = refreshToken
	}
	digest := sha256.Sum256([]byte(key))
	return oauthManagedPrefix + hex.EncodeToString(digest[:])
}

// resolveVerificationURL turns the console's verification_uri_complete into an
// absolute http(s) URL: it may be relative to the server origin.
func resolveVerificationURL(server, verification string) (string, error) {
	u, err := url.Parse(verification)
	if err != nil {
		return "", fmt.Errorf("opencode-go login start failed: invalid verification URL")
	}
	if !u.IsAbs() {
		base, baseErr := url.Parse(server)
		if baseErr != nil {
			return "", fmt.Errorf("opencode-go login start failed: invalid server URL")
		}
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("opencode-go login start failed: verification URL is not http(s)")
	}
	return u.String(), nil
}

// StartLogin begins a device authorization: it requests a device code from the
// console and returns the URL for the operator to open plus the opaque state
// the host polls back.
func (p authProvider) StartLogin(ctx context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	m, err := p.loginManager()
	if err != nil {
		return pluginapi.AuthLoginStartResponse{}, err
	}
	server, userAgent := m.loginConfig()
	if override, ok := req.Metadata["server"].(string); ok {
		if base, baseErr := canonicalServer(override); baseErr == nil && base != "" {
			server = base
		}
	}
	body, _ := json.Marshal(map[string]any{
		"client_id":          opencodeOAuthClientID,
		"supports_org_scope": true,
	})
	headers := jsonHeaders()
	headers.Set("User-Agent", userAgent)
	resp, err := m.bridge.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodPost, URL: server + oauthDevicePath, Headers: headers, Body: body,
	})
	if err != nil {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("opencode-go login start failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("opencode-go login start failed: upstream status %d", resp.StatusCode)
	}
	var decoded deviceCodeResp
	if err := json.Unmarshal(resp.Body, &decoded); err != nil ||
		strings.TrimSpace(decoded.DeviceCode) == "" {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("opencode-go login start failed: invalid device code response")
	}
	verification := strings.TrimSpace(decoded.VerificationURIComplete)
	if verification == "" {
		verification = strings.TrimSpace(decoded.VerificationURI)
	}
	if verification == "" {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("opencode-go login start failed: no verification URL")
	}
	verification, err = resolveVerificationURL(server, verification)
	if err != nil {
		return pluginapi.AuthLoginStartResponse{}, err
	}
	expiresIn := decoded.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 600
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
	state, err := randomToken(32)
	if err != nil {
		return pluginapi.AuthLoginStartResponse{}, err
	}
	m.loginMu.Lock()
	m.pruneExpiredLoginsLocked()
	if len(m.logins) >= maxConcurrentLogins {
		m.loginMu.Unlock()
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("too many concurrent opencode-go sign-ins; retry shortly")
	}
	m.logins[state] = deviceLogin{
		server:     server,
		userAgent:  userAgent,
		deviceCode: decoded.DeviceCode,
		expiresAt:  expiresAt,
	}
	m.loginMu.Unlock()
	metadata := map[string]any{}
	if strings.TrimSpace(decoded.UserCode) != "" {
		metadata["instructions"] = "Open the URL and enter code: " + decoded.UserCode
	}
	return pluginapi.AuthLoginStartResponse{
		Provider: ProviderID, URL: verification, State: state, ExpiresAt: expiresAt, Metadata: metadata,
	}, nil
}

// PollLogin advances a device authorization: it posts the device_code to the
// token endpoint and reports pending until the operator has signed in, then
// completes the credential.
func (p authProvider) PollLogin(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	m, err := p.loginManager()
	if err != nil {
		return pluginapi.AuthLoginPollResponse{}, err
	}
	state := strings.TrimSpace(req.State)
	m.loginMu.Lock()
	login, found := m.logins[state]
	if found && time.Now().After(login.expiresAt) {
		delete(m.logins, state)
		found = false
	}
	m.loginMu.Unlock()
	if !found {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "sign-in attempt unknown or expired",
		}, nil
	}
	body, _ := json.Marshal(map[string]any{
		"grant_type":  grantDeviceCode,
		"device_code": login.deviceCode,
		"client_id":   opencodeOAuthClientID,
	})
	tokenHeaders := jsonHeaders()
	tokenHeaders.Set("User-Agent", login.userAgent)
	resp, err := m.bridge.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodPost, URL: login.server + oauthTokenPath, Headers: tokenHeaders, Body: body,
	})
	if err != nil {
		// A transport failure is transient: the operator may still be signing
		// in, so keep polling rather than dropping the attempt.
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "opencode-go sign-in status temporarily unavailable",
		}, nil
	}
	var decoded deviceTokenResp
	if err := json.Unmarshal(resp.Body, &decoded); err != nil {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "invalid token response from console",
		}, nil
	}
	if decoded.AccessToken == "" {
		switch decoded.Error {
		case deviceErrorPending, deviceErrorSlowDown:
			return pluginapi.AuthLoginPollResponse{
				Status:  pluginapi.AuthLoginStatusPending,
				Message: "waiting for OpenCode Console sign-in",
			}, nil
		default:
			m.dropLogin(state)
			reason := strings.TrimSpace(decoded.Error)
			if reason == "" {
				reason = fmt.Sprintf("upstream status %d", resp.StatusCode)
			}
			return pluginapi.AuthLoginPollResponse{
				Status:  pluginapi.AuthLoginStatusError,
				Message: "sign-in failed: " + reason,
			}, nil
		}
	}
	// Complete: fetch identity best-effort, build the credential, drop the
	// in-flight attempt.
	accountID, email, orgID, orgName, idErr := m.fetchOAuthIdentity(ctx, login.server, login.userAgent, decoded.AccessToken, decoded.OrgID)
	if idErr != nil {
		m.dropLogin(state)
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: idErr.Error()}, nil
	}
	expiresIn := decoded.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 30 * 24 * 3600
	}
	rec := oauthRecord{
		Type:           ProviderID,
		ID:             oauthRecordID(accountID, email, decoded.RefreshToken),
		Label:          oauthLabel(accountID, email, decoded.RefreshToken),
		CredentialType: credentialTypeOAuth,
		Server:         login.server,
		ClientID:       opencodeOAuthClientID,
		AccessToken:    decoded.AccessToken,
		RefreshToken:   decoded.RefreshToken,
		ExpiresAt:      time.Now().Add(time.Duration(expiresIn) * time.Second).Unix(),
		OrgID:          orgID,
		OrgName:        orgName,
		AccountID:      accountID,
		Email:          email,
	}
	m.dropLogin(state)
	return pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusSuccess,
		Message: "OpenCode Console credential created",
		Auth:    rec.toAuthData(),
	}, nil
}

// RefreshAuth renews an OAuth credential via grant_type refresh_token; API-key
// records pass through untouched. The route on credential type is explicit:
// only a record flagged oauth (or carrying a refresh token) is renewed.
func (p authProvider) RefreshAuth(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	debugTrace("auth refresh request provider=%s id=%s storage_json_bytes=%d attr_names=%v metadata_names=%v", req.AuthProvider, req.AuthID, len(req.StorageJSON), mapKeys(req.Attributes), mapKeys(req.Metadata))
	if strings.TrimSpace(req.Attributes["credential_type"]) != credentialTypeOAuth {
		return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{
			Provider: req.AuthProvider, ID: req.AuthID, StorageJSON: req.StorageJSON, Metadata: req.Metadata, Attributes: req.Attributes,
		}}, nil
	}
	m, err := p.loginManager()
	if err != nil {
		return pluginapi.AuthRefreshResponse{}, err
	}
	var raw parsedRecord
	if err := json.Unmarshal(req.StorageJSON, &raw); err != nil {
		// Cannot renew: the stored credential is unreadable. Mark disabled so
		// the host stops scheduling it; the operator re-logins.
		data := unchangedAuth(req)
		data.Disabled = true
		return pluginapi.AuthRefreshResponse{Auth: data, NextRefreshAfter: time.Now().UTC()}, nil
	}
	norm, normErr := raw.normalizeCredential()
	if normErr != nil || norm.credentialType != credentialTypeOAuth || strings.TrimSpace(norm.refreshToken) == "" {
		// Cannot renew: not an OAuth credential, or no refresh token to
		// redeem. Mark disabled so the host stops scheduling an unrecoverable
		// credential; the operator re-logins.
		data := unchangedAuth(req)
		data.Disabled = true
		return pluginapi.AuthRefreshResponse{Auth: data, NextRefreshAfter: time.Now().UTC()}, nil
	}
	server := norm.server
	if strings.TrimSpace(server) == "" {
		server, _ = m.loginConfig()
	}
	clientID := norm.clientID
	if strings.TrimSpace(clientID) == "" {
		clientID = opencodeOAuthClientID
	}
	_, userAgent := m.loginConfig()
	body, _ := json.Marshal(map[string]any{
		"grant_type":    grantRefreshToken,
		"refresh_token": norm.refreshToken,
		"client_id":     clientID,
	})
	refreshHeaders := jsonHeaders()
	refreshHeaders.Set("User-Agent", userAgent)
	resp, err := m.bridge.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodPost, URL: server + oauthTokenPath, Headers: refreshHeaders, Body: body,
	})
	var decoded deviceTokenResp
	if err != nil {
		// Transport failure: retry soon, keep the credential as-is.
		return pluginapi.AuthRefreshResponse{Auth: unchangedAuth(req), NextRefreshAfter: time.Now().UTC()}, nil
	}
	if resp.StatusCode != http.StatusOK {
		// A 400 carrying invalid_grant means the console revoked the session
		// or refresh token; only a new device login recovers, so disable the
		// credential. Any other failure is not proof the token is dead.
		_ = json.Unmarshal(resp.Body, &decoded)
		if resp.StatusCode == http.StatusBadRequest && strings.TrimSpace(decoded.Error) == deviceErrorInvalid {
			data := unchangedAuth(req)
			data.Disabled = true
			return pluginapi.AuthRefreshResponse{Auth: data, NextRefreshAfter: time.Now().UTC()}, nil
		}
		return pluginapi.AuthRefreshResponse{Auth: unchangedAuth(req), NextRefreshAfter: time.Now().UTC()}, nil
	}
	if jsonErr := json.Unmarshal(resp.Body, &decoded); jsonErr != nil || strings.TrimSpace(decoded.AccessToken) == "" {
		return pluginapi.AuthRefreshResponse{}, fmt.Errorf("opencode-go refresh failed: invalid token response")
	}
	// Rebuild the canonical record: the renewed secrets plus the identity the
	// stored credential carried (a CLI-native shape migrates to ours here).
	rec := oauthRecord{
		Type:           ProviderID,
		ID:             req.AuthID,
		Label:          firstNonEmpty(raw.Label, oauthLabel(norm.accountID, norm.email, norm.refreshToken)),
		CredentialType: credentialTypeOAuth,
		Server:         server,
		ClientID:       clientID,
		AccessToken:    decoded.AccessToken,
		RefreshToken:   norm.refreshToken,
		OrgID:          norm.orgID,
		OrgName:        norm.orgName,
		AccountID:      norm.accountID,
		Email:          norm.email,
	}
	if strings.TrimSpace(decoded.RefreshToken) != "" {
		rec.RefreshToken = decoded.RefreshToken
	}
	if strings.TrimSpace(decoded.OrgID) != "" {
		rec.OrgID = decoded.OrgID
	}
	if decoded.ExpiresIn > 0 {
		rec.ExpiresAt = time.Now().Add(time.Duration(decoded.ExpiresIn) * time.Second).Unix()
	} else {
		// Console omitted an expiry; assume a conservative 30 days so the host
		// still schedules a refresh instead of leaving the token undated.
		rec.ExpiresAt = time.Now().Add(30 * 24 * 3600 * time.Second).Unix()
	}
	data := rec.toAuthData()
	data.ID = req.AuthID
	data.FileName = "" // host preserves the existing file name
	data.Disabled = false
	return pluginapi.AuthRefreshResponse{Auth: data, NextRefreshAfter: refreshAfter(rec.ExpiresAt)}, nil
}

// unchangedAuth returns the request's credential as an AuthData passthrough,
// preserving the host-managed metadata and attributes.
func unchangedAuth(req pluginapi.AuthRefreshRequest) pluginapi.AuthData {
	return pluginapi.AuthData{
		Provider: req.AuthProvider, ID: req.AuthID, StorageJSON: req.StorageJSON, Metadata: req.Metadata, Attributes: req.Attributes,
	}
}

// toAuthData renders the record as the host's AuthData: the access token is
// carried in the api_key attribute (the bearer the executor sends), the full
// record is the persisted StorageJSON, and the expiry schedules a refresh.
func (rec oauthRecord) toAuthData() pluginapi.AuthData {
	storage, _ := json.Marshal(rec)
	attrs := map[string]string{"api_key": rec.AccessToken, "credential_type": credentialTypeOAuth}
	// The executor reads the org id from attributes to scope the credential to
	// its signed-in workspace (the free-tier/Zen entitlement). Also emitted in
	// the host's header: attribute form for parity with the sibling plugin.
	if strings.TrimSpace(rec.OrgID) != "" {
		attrs["org_id"] = rec.OrgID
		attrs["header:x-opencode-org-id"] = rec.OrgID
	}
	data := pluginapi.AuthData{
		Provider: ProviderID, ID: rec.ID, FileName: rec.ID + ".json", Label: rec.Label,
		StorageJSON: storage,
		Attributes:  attrs,
	}
	if rec.ExpiresAt != 0 {
		data.NextRefreshAfter = refreshAfter(rec.ExpiresAt)
	}
	return data
}

// oauthLabel names a credential from identity when known, else a stable short
// hash of the refresh token (never the token itself).
func oauthLabel(accountID, email, refreshToken string) string {
	for _, ident := range []string{email, accountID} {
		if strings.TrimSpace(ident) != "" {
			return "OpenCode OAuth " + strings.TrimSpace(ident)
		}
	}
	digest := sha256.Sum256([]byte(refreshToken))
	return "OpenCode OAuth credential " + hex.EncodeToString(digest[:])[:12]
}

// fetchOAuthIdentity resolves the signed-in account and its organization from
// the console. A missing organization when the token pinned one is an error;
// other fetch failures are swallowed and reported as empty identity so a
// transient console blip cannot block a completed sign-in.
func (m *Manager) fetchOAuthIdentity(ctx context.Context, server, userAgent, accessToken, wantOrgID string) (accountID, email, orgID, orgName string, err error) {
	bearerHeaders := http.Header{
		"Authorization": []string{"Bearer " + accessToken},
		"Accept":        []string{"application/json"},
		"User-Agent":    []string{userAgent},
	}
	if r, e := m.bridge.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodGet, URL: server + oauthUserPath, Headers: bearerHeaders,
	}); e == nil && r.StatusCode == http.StatusOK {
		var u struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		}
		if json.Unmarshal(r.Body, &u) == nil {
			accountID, email = u.ID, u.Email
		}
	}
	var orgs []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if r, e := m.bridge.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodGet, URL: server + oauthOrgsPath, Headers: bearerHeaders,
	}); e == nil && r.StatusCode == http.StatusOK {
		_ = json.Unmarshal(r.Body, &orgs)
	}
	sort.Slice(orgs, func(i, j int) bool {
		if orgs[i].Name != orgs[j].Name {
			return orgs[i].Name < orgs[j].Name
		}
		return orgs[i].ID < orgs[j].ID
	})
	if wantOrgID != "" {
		for _, o := range orgs {
			if o.ID == wantOrgID {
				orgID, orgName = o.ID, o.Name
				return accountID, email, orgID, orgName, nil
			}
		}
		return "", "", "", "", fmt.Errorf("OpenCode organization not found for this sign-in; reconnect to retry")
	}
	if len(orgs) > 0 {
		orgID, orgName = orgs[0].ID, orgs[0].Name
	}
	return accountID, email, orgID, orgName, nil
}

// dropLogin removes one in-flight attempt under the login lock.
func (m *Manager) dropLogin(state string) {
	m.loginMu.Lock()
	defer m.loginMu.Unlock()
	delete(m.logins, state)
}

// pruneExpiredLoginsLocked evicts lapsed attempts. Callers hold m.loginMu.
func (m *Manager) pruneExpiredLoginsLocked() {
	now := time.Now()
	for state, login := range m.logins {
		if now.After(login.expiresAt) {
			delete(m.logins, state)
		}
	}
}

// canonicalServer normalizes an operator-supplied console server to
// origin+path, rejecting non-http(s) values.
func canonicalServer(input string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(input))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid server URL")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}
