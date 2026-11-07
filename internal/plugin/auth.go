package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// authProvider implements the host's AuthProvider for this plugin. ParseAuth is
// stateless; the login and refresh methods route on the credential type and use
// the owning manager's host bridge for upstream traffic.
type authProvider struct {
	manager *Manager
}

var _ pluginapi.AuthProvider = authProvider{}

func (authProvider) Identifier() string { return ProviderID }

// parsedRecord is one opencode-go auth file. A record is EITHER an API key
// (service account) OR an OAuth console token: the two are distinguished by the
// credential_type field (or, for legacy API-key files, by which of api_key /
// access_token is present).
//
// It also accepts OpenCode CLI's native console-credential shape — top-level
// access/refresh keys with the server under metadata — so a credential file
// written by `opencode auth login` is adopted as-is (recognized only when the
// server points at opencode.ai, never by shape alone).
type parsedRecord struct {
	Type           string `json:"type"`
	Provider       string `json:"provider"`
	ID             string `json:"id"`
	Label          string `json:"label"`
	CredentialType string `json:"credential_type"`
	APIKey         string `json:"api_key"`
	// Our canonical OAuth fields.
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // unix seconds
	Server       string `json:"server"`
	ClientID     string `json:"client_id"`
	OrgID        string `json:"org_id"`
	OrgName      string `json:"org_name"`
	AccountID    string `json:"account_id"`
	Email        string `json:"email"`
	// OpenCode CLI native aliases.
	Access      string `json:"access"`
	Refresh     string `json:"refresh"`
	Expires     string `json:"expires"` // RFC3339
	ExpiresAtMs int64  `json:"expires_at_ms"`
	Metadata    struct {
		Server    string `json:"server"`
		AccountID string `json:"accountID"`
		Email     string `json:"email"`
		OrgID     string `json:"orgID"`
		OrgName   string `json:"orgName"`
	} `json:"metadata"`
}

// normalizedCredential is one record resolved to its type, bearer secret, org
// scope and expiry, regardless of which shape it was stored in.
type normalizedCredential struct {
	credentialType string
	bearer         string
	refreshToken   string
	server         string
	clientID       string
	orgID          string
	orgName        string
	accountID      string
	email          string
	expiresAt      int64 // unix seconds; 0 = undated
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// normalizeCredential resolves the credential type and the secrets carried in
// the record. The executor sends the bearer (api_key attribute — the API key
// OR the OAuth access token) upstream; refresh and parse route on the type.
func (raw parsedRecord) normalizeCredential() (normalizedCredential, error) {
	norm := normalizedCredential{
		refreshToken: firstNonEmpty(raw.RefreshToken, raw.Refresh),
		server:       firstNonEmpty(raw.Server, raw.Metadata.Server),
		clientID:     raw.ClientID,
		orgID:        firstNonEmpty(raw.OrgID, raw.Metadata.OrgID),
		orgName:      firstNonEmpty(raw.OrgName, raw.Metadata.OrgName),
		accountID:    firstNonEmpty(raw.AccountID, raw.Metadata.AccountID),
		email:        firstNonEmpty(raw.Email, raw.Metadata.Email),
	}
	switch {
	case raw.ExpiresAtMs > 0:
		norm.expiresAt = raw.ExpiresAtMs / 1000
	case raw.ExpiresAt != 0:
		norm.expiresAt = raw.ExpiresAt
	case raw.Expires != "":
		if t, err := time.Parse(time.RFC3339, raw.Expires); err == nil {
			norm.expiresAt = t.Unix()
		}
	}
	switch strings.TrimSpace(raw.CredentialType) {
	case credentialTypeOAuth:
		if norm.bearer = firstNonEmpty(raw.AccessToken, raw.Access); norm.bearer == "" {
			return normalizedCredential{}, fmt.Errorf("opencode-go oauth record has no access token")
		}
		norm.credentialType = credentialTypeOAuth
		return norm, nil
	default:
		if norm.bearer = strings.TrimSpace(raw.APIKey); norm.bearer != "" {
			norm.credentialType = credentialTypeAPIKey
			return norm, nil
		}
		// CLI-native shape: an access token whose server is the opencode.ai
		// console. Shape alone never claims a record — the server check keeps
		// another provider's credential files out of reach.
		if acc := strings.TrimSpace(raw.Access); acc != "" &&
			strings.Contains(strings.ToLower(norm.server), "opencode.ai") {
			norm.bearer = acc
			norm.credentialType = credentialTypeOAuth
			return norm, nil
		}
		if norm.bearer = strings.TrimSpace(raw.AccessToken); norm.bearer != "" {
			norm.credentialType = credentialTypeOAuth
			return norm, nil
		}
		return normalizedCredential{}, fmt.Errorf("opencode-go auth record has no api key")
	}
}

func (p authProvider) ParseAuth(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	debugTrace("auth parse request provider=%s file=%s raw_bytes=%d", req.Provider, req.FileName, len(req.RawJSON))
	var raw parsedRecord
	if err := json.Unmarshal(req.RawJSON, &raw); err != nil {
		if req.Provider == ProviderID {
			return pluginapi.AuthParseResponse{}, fmt.Errorf("opencode-go auth record has invalid JSON")
		}
		return pluginapi.AuthParseResponse{}, nil
	}
	if req.Provider != "" && req.Provider != ProviderID {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	// A CLI-native console credential (access token + opencode.ai server under
	// metadata) is claimed even when its type field is the console's own
	// "oauth" rather than this provider; everything else must name us.
	native := strings.TrimSpace(raw.Access) != "" &&
		strings.Contains(strings.ToLower(raw.Metadata.Server), "opencode.ai")
	if !native && raw.Type != ProviderID && raw.Provider != ProviderID {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	norm, err := raw.normalizeCredential()
	if err != nil {
		return pluginapi.AuthParseResponse{}, err
	}
	if raw.ID == "" {
		raw.ID = req.FileName
	}
	auth := pluginapi.AuthData{
		Provider: ProviderID, ID: raw.ID, FileName: req.FileName, Label: firstNonEmpty(raw.Label, norm.email), StorageJSON: req.RawJSON,
		Attributes: map[string]string{"api_key": norm.bearer, "credential_type": norm.credentialType},
	}
	// An OAuth credential scopes itself to its signed-in workspace via the org
	// id; the executor reads it from attributes to send x-opencode-org-id.
	if norm.credentialType == credentialTypeOAuth {
		if norm.orgID != "" {
			auth.Attributes["org_id"] = norm.orgID
			auth.Attributes["header:x-opencode-org-id"] = norm.orgID
		}
		// A token with a known expiry tells the host when to refresh it; an
		// API key has no refresh schedule.
		if norm.expiresAt != 0 {
			auth.NextRefreshAfter = refreshAfter(norm.expiresAt)
		}
	}
	debugTrace("auth parse handled provider=%s file=%s id=%s type=%s api_key_present=%t api_key_length=%d", req.Provider, req.FileName, raw.ID, norm.credentialType, norm.bearer != "", len(norm.bearer))
	return pluginapi.AuthParseResponse{Handled: true, Auth: auth}, nil
}

func mapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
