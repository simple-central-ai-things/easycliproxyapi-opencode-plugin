package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// loginFake is a scripted host.http.do responder for the console device flow.
// It records every requested URL and serves canned bodies per endpoint.
type loginFake struct {
	mu             sync.Mutex
	calls          []string
	tokenResponses [][]byte // consumed in call order at /auth/device/token
	tokenStatuses  []int
	tokenCursor    int
	deviceStatus   int
	deviceBody     []byte
	userBody       []byte
	orgsBody       []byte
}

var jsonContentType = http.Header{"Content-Type": []string{"application/json"}}

func (f *loginFake) call(method string, payload []byte) ([]byte, error) {
	if method == pluginabi.MethodHostLog {
		return hostOK(map[string]any{}), nil
	}
	if method != pluginabi.MethodHostHTTPDo {
		return hostOK(map[string]any{}), nil
	}
	var wire struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return hostErr("test", "undecodable payload"), nil
	}
	u, err := url.Parse(wire.URL)
	if err != nil {
		return hostErr("test", "bad url"), nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, wire.URL)
	resp := pluginapi.HTTPResponse{StatusCode: http.StatusOK, Headers: jsonContentType}
	switch {
	case strings.HasSuffix(u.Path, "/auth/device/code"):
		resp.StatusCode = f.deviceStatus
		resp.Body = f.deviceBody
	case strings.HasSuffix(u.Path, "/auth/device/token"):
		idx := f.tokenCursor
		f.tokenCursor++
		body := []byte(`{}`)
		status := http.StatusOK
		if idx < len(f.tokenResponses) {
			body = f.tokenResponses[idx]
		}
		if idx < len(f.tokenStatuses) {
			status = f.tokenStatuses[idx]
		}
		resp.StatusCode = status
		resp.Body = body
	case strings.HasSuffix(u.Path, "/api/user"):
		resp.Body = f.userBody
	case strings.HasSuffix(u.Path, "/api/orgs"):
		resp.Body = f.orgsBody
	default:
		resp.StatusCode = http.StatusNotFound
	}
	return hostOK(resp), nil
}

func (f *loginFake) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func loginManager(t *testing.T, fake *loginFake) *Manager {
	t.Helper()
	return NewManager(NewHostBridge(fake.call))
}

const deviceCodeJSON = `{"device_code":"dev-1","user_code":"ABC123",` +
	`"verification_uri_complete":"/device?user_code=ABC123","expires_in":600,"interval":5}`

func TestParseOAuthRecord(t *testing.T) {
	raw := []byte(`{"type":"opencode-go","id":"opencode-go-oauth-abc","credential_type":"oauth",` +
		`"access_token":"oc-access-1","refresh_token":"oc-refresh-1",` +
		`"expires_at":` + itoa(time.Now().Add(time.Hour).Unix()) + `,"server":"https://opencode.ai/console","org_id":"org-1"}`)
	parsed, err := (authProvider{}).ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		Provider: ProviderID, FileName: "opencode-go-oauth-abc.json", RawJSON: raw,
	})
	if err != nil || !parsed.Handled {
		t.Fatalf("parse oauth = %#v err=%v", parsed, err)
	}
	if parsed.Auth.Attributes["api_key"] != "oc-access-1" {
		t.Fatalf("oauth bearer attr = %q", parsed.Auth.Attributes["api_key"])
	}
	if parsed.Auth.Attributes["credential_type"] != credentialTypeOAuth {
		t.Fatalf("credential_type attr = %q", parsed.Auth.Attributes["credential_type"])
	}
	if parsed.Auth.NextRefreshAfter.IsZero() {
		t.Fatal("expected NextRefreshAfter for oauth with expiry")
	}
}

func TestParseAPIKeyRecordUnchanged(t *testing.T) {
	raw := []byte(`{"type":"opencode-go","id":"k","api_key":"oc_sk_plain"}`)
	parsed, err := (authProvider{}).ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		Provider: ProviderID, FileName: "k.json", RawJSON: raw,
	})
	if err != nil || !parsed.Handled {
		t.Fatalf("parse api key = %#v err=%v", parsed, err)
	}
	if parsed.Auth.Attributes["api_key"] != "oc_sk_plain" ||
		parsed.Auth.Attributes["credential_type"] != credentialTypeAPIKey {
		t.Fatalf("api key attrs = %v", parsed.Auth.Attributes)
	}
	if !parsed.Auth.NextRefreshAfter.IsZero() {
		t.Fatal("api key must not schedule a refresh")
	}
}

func TestParseOAuthRecordWithoutAccessTokenFails(t *testing.T) {
	raw := []byte(`{"type":"opencode-go","credential_type":"oauth","refresh_token":"r"}`)
	parsed, err := (authProvider{}).ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		Provider: ProviderID, RawJSON: raw,
	})
	if err == nil || parsed.Handled {
		t.Fatalf("expected failure, got %#v err=%v", parsed, err)
	}
}

func TestDeviceLoginFlow(t *testing.T) {
	fake := &loginFake{
		deviceStatus: http.StatusOK,
		deviceBody:   []byte(deviceCodeJSON),
		tokenResponses: [][]byte{
			[]byte(`{"error":"authorization_pending"}`),
			[]byte(`{"access_token":"oc-access-flow","refresh_token":"oc-refresh-flow","expires_in":3600,"org_id":""}`),
		},
		userBody: []byte(`{"id":"acct-1","email":"dev@example.com"}`),
		orgsBody: []byte(`[{"id":"org-9","name":"Team Nine"}]`),
	}
	m := loginManager(t, fake)
	p := authProvider{manager: m}

	start, err := p.StartLogin(context.Background(), pluginapi.AuthLoginStartRequest{Provider: ProviderID})
	if err != nil {
		t.Fatalf("start = %v", err)
	}
	if start.Provider != ProviderID || start.URL == "" || start.State == "" {
		t.Fatalf("start response = %+v", start)
	}
	if strings.Contains(start.State, "dev-1") {
		t.Fatalf("state leaked the device_code: %q", start.State)
	}
	if !strings.Contains(start.URL, "/device?user_code=ABC123") {
		t.Fatalf("verification url = %q", start.URL)
	}

	pending, err := p.PollLogin(context.Background(), pluginapi.AuthLoginPollRequest{State: start.State})
	if err != nil {
		t.Fatalf("poll pending = %v", err)
	}
	if pending.Status != pluginapi.AuthLoginStatusPending {
		t.Fatalf("expected pending, got %+v", pending)
	}

	done, err := p.PollLogin(context.Background(), pluginapi.AuthLoginPollRequest{State: start.State})
	if err != nil {
		t.Fatalf("poll done = %v", err)
	}
	if done.Status != pluginapi.AuthLoginStatusSuccess || done.Auth.ID == "" ||
		done.Auth.Attributes["api_key"] != "oc-access-flow" ||
		done.Auth.Attributes["credential_type"] != credentialTypeOAuth {
		t.Fatalf("done = %+v", done)
	}
	if done.Auth.FileName != done.Auth.ID+".json" || !strings.HasPrefix(done.Auth.FileName, oauthManagedPrefix) {
		t.Fatalf("oauth file name = %q", done.Auth.FileName)
	}
	var rec oauthRecord
	if err := json.Unmarshal(done.Auth.StorageJSON, &rec); err != nil || rec.Email != "dev@example.com" || rec.OrgID != "org-9" {
		t.Fatalf("storage record = %+v err=%v", rec, err)
	}

	// The attempt is consumed: a third poll reports error.
	again, err := p.PollLogin(context.Background(), pluginapi.AuthLoginPollRequest{State: start.State})
	if err != nil || again.Status != pluginapi.AuthLoginStatusError {
		t.Fatalf("third poll = %+v err=%v", again, err)
	}
}

func TestDeviceLoginUnknownState(t *testing.T) {
	m := loginManager(t, &loginFake{})
	p := authProvider{manager: m}
	resp, err := p.PollLogin(context.Background(), pluginapi.AuthLoginPollRequest{State: "nope"})
	if err != nil {
		t.Fatalf("poll unknown = %v", err)
	}
	if resp.Status != pluginapi.AuthLoginStatusError {
		t.Fatalf("unknown state = %+v", resp)
	}
}

func TestDeviceLoginStartUpstreamFailure(t *testing.T) {
	fake := &loginFake{deviceStatus: http.StatusBadGateway, deviceBody: []byte(`oops`)}
	m := loginManager(t, fake)
	p := authProvider{manager: m}
	if _, err := p.StartLogin(context.Background(), pluginapi.AuthLoginStartRequest{Provider: ProviderID}); err == nil {
		t.Fatal("expected start failure on upstream 502")
	}
}

func TestParseCLINativeCredentialShape(t *testing.T) {
	// A credential written by `opencode auth login` stores access/refresh keys
	// with the server under metadata; it is adopted when the server points at
	// opencode.ai (never by shape alone).
	raw := []byte(`{"type":"oauth","access":"cli-access","refresh":"cli-refresh",` +
		`"expires":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `",` +
		`"metadata":{"server":"https://opencode.ai/console","accountID":"acct-1","email":"cli@example.com","orgID":"org-1"}}`)
	parsed, err := (authProvider{}).ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		Provider: ProviderID, FileName: "opencode-cli-native.json", RawJSON: raw,
	})
	if err != nil || !parsed.Handled {
		t.Fatalf("parse cli-native = %#v err=%v", parsed, err)
	}
	if parsed.Auth.Attributes["api_key"] != "cli-access" ||
		parsed.Auth.Attributes["credential_type"] != credentialTypeOAuth ||
		parsed.Auth.Attributes["org_id"] != "org-1" {
		t.Fatalf("cli-native attrs = %v", parsed.Auth.Attributes)
	}
	if parsed.Auth.NextRefreshAfter.IsZero() {
		t.Fatal("cli-native oauth with RFC3339 expiry must schedule a refresh")
	}

	// The same shape pointing at a foreign server is not claimed.
	foreign := []byte(`{"type":"opencode-go","access":"x-access","metadata":{"server":"https://other.example.com/console"}}`)
	if _, err := (authProvider{}).ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		Provider: ProviderID, RawJSON: foreign,
	}); err == nil {
		t.Fatal("foreign-server access token must not parse as opencode-go")
	}
}

func TestRefreshOAuthToken(t *testing.T) {
	rec := oauthRecord{
		Type: ProviderID, ID: "opencode-go-oauth-r1", CredentialType: credentialTypeOAuth,
		Server: opencodeConsoleServer, ClientID: opencodeOAuthClientID,
		AccessToken: "old", RefreshToken: "refresh-1", ExpiresAt: time.Now().Unix(),
		Email: "dev@example.com",
	}
	storage, _ := json.Marshal(rec)
	fake := &loginFake{
		tokenResponses: [][]byte{[]byte(`{"access_token":"new-access","refresh_token":"refresh-2","expires_in":3600}`)},
	}
	m := loginManager(t, fake)
	p := authProvider{manager: m}
	resp, err := p.RefreshAuth(context.Background(), pluginapi.AuthRefreshRequest{
		AuthID: rec.ID, AuthProvider: ProviderID, StorageJSON: storage,
		Attributes: map[string]string{"api_key": "old", "credential_type": credentialTypeOAuth},
	})
	if err != nil {
		t.Fatalf("refresh = %v", err)
	}
	if resp.Auth.Attributes["api_key"] != "new-access" {
		t.Fatalf("refreshed bearer = %q", resp.Auth.Attributes["api_key"])
	}
	var updated oauthRecord
	if err := json.Unmarshal(resp.Auth.StorageJSON, &updated); err != nil || updated.RefreshToken != "refresh-2" {
		t.Fatalf("updated record = %+v err=%v", updated, err)
	}
	if resp.NextRefreshAfter.IsZero() {
		t.Fatal("refresh must schedule the next refresh")
	}
}

func TestRefreshAPIKeyPassThrough(t *testing.T) {
	m := loginManager(t, &loginFake{})
	p := authProvider{manager: m}
	attrs := map[string]string{"api_key": "oc_sk_k", "credential_type": credentialTypeAPIKey}
	resp, err := p.RefreshAuth(context.Background(), pluginapi.AuthRefreshRequest{
		AuthID: "k", AuthProvider: ProviderID, StorageJSON: []byte(`{"api_key":"oc_sk_k"}`), Attributes: attrs,
	})
	if err != nil {
		t.Fatalf("refresh api key = %v", err)
	}
	if resp.Auth.Attributes["api_key"] != "oc_sk_k" {
		t.Fatalf("pass-through bearer = %q", resp.Auth.Attributes["api_key"])
	}
}

func TestRefreshInvalidGrantDisables(t *testing.T) {
	rec := oauthRecord{Type: ProviderID, ID: "opencode-go-oauth-d", CredentialType: credentialTypeOAuth,
		Server: opencodeConsoleServer, ClientID: opencodeOAuthClientID, RefreshToken: "dead"}
	storage, _ := json.Marshal(rec)
	fake := &loginFake{
		tokenStatuses:  []int{http.StatusBadRequest},
		tokenResponses: [][]byte{[]byte(`{"error":"invalid_grant"}`)},
	}
	m := loginManager(t, fake)
	p := authProvider{manager: m}
	resp, err := p.RefreshAuth(context.Background(), pluginapi.AuthRefreshRequest{
		AuthID: rec.ID, AuthProvider: ProviderID, StorageJSON: storage,
		Attributes: map[string]string{"api_key": "x", "credential_type": credentialTypeOAuth},
	})
	if err != nil {
		t.Fatalf("refresh invalid grant = %v", err)
	}
	if !resp.Auth.Disabled {
		t.Fatal("revoked credential must be disabled")
	}
}

// TestLoginDispatchWired routes a full StartLogin through the C-ABI dispatcher
// with a scripted bridge, confirming HandleCall wires login to the handler.
func TestLoginDispatchWired(t *testing.T) {
	fake := &loginFake{deviceStatus: http.StatusOK, deviceBody: []byte(deviceCodeJSON)}
	m := loginManager(t, fake)
	req := pluginapi.AuthLoginStartRequest{Provider: ProviderID}
	raw, err := m.HandleCall(pluginabi.MethodAuthLoginStart, mustJSON(req))
	if err != nil {
		t.Fatal(err)
	}
	var start pluginapi.AuthLoginStartResponse
	decodeResult(t, raw, &start)
	if start.URL == "" || start.State == "" {
		t.Fatalf("dispatch start = %+v", start)
	}
}

// TestRefreshCLI_NativeShapeRenewsAndMigrates confirms a credential stored in
// the OpenCode CLI's native shape (access/refresh keys, server under
// metadata) renews and is rewritten in the canonical plugin shape.
func TestRefreshCLINativeShapeRenewsAndMigrates(t *testing.T) {
	storage := []byte(`{"type":"oauth","access":"cli-old","refresh":"cli-refresh",` +
		`"expires":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `",` +
		`"metadata":{"server":"https://opencode.ai/console","email":"cli@example.com","orgID":"org-7"}}`)
	fake := &loginFake{
		tokenResponses: [][]byte{[]byte(`{"access_token":"cli-new","refresh_token":"cli-refresh-2","expires_in":3600}`)},
	}
	m := loginManager(t, fake)
	p := authProvider{manager: m}
	resp, err := p.RefreshAuth(context.Background(), pluginapi.AuthRefreshRequest{
		AuthID: "opencode-cli-native", AuthProvider: ProviderID, StorageJSON: storage,
		Attributes: map[string]string{"api_key": "cli-old", "credential_type": credentialTypeOAuth},
	})
	if err != nil {
		t.Fatalf("refresh cli-native = %v", err)
	}
	if resp.Auth.Attributes["api_key"] != "cli-new" {
		t.Fatalf("migrated bearer = %q", resp.Auth.Attributes["api_key"])
	}
	var updated oauthRecord
	if err := json.Unmarshal(resp.Auth.StorageJSON, &updated); err != nil {
		t.Fatalf("updated record = %v", err)
	}
	if updated.CredentialType != credentialTypeOAuth || updated.Server != opencodeConsoleServer ||
		updated.RefreshToken != "cli-refresh-2" || updated.OrgID != "org-7" {
		t.Fatalf("migrated record = %+v", updated)
	}
}

func itoa(v int64) string {
	return jsonNumber(v)
}

func jsonNumber(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
