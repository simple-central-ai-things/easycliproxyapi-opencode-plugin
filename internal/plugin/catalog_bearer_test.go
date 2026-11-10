package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"opencode-go-cliproxyapi/internal/config"
)

// oauthHostFake is a host caller that answers only the two auth reads
// catalogFetchBearer performs: a listing of one OAuth record and its JSON.
func oauthHostFake(oauthName, oauthJSON string, listOK bool) RawCaller {
	return func(method string, payload []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			if !listOK {
				return hostErr("auth_unavailable", "no auth dir"), nil
			}
			return hostOK(hostAuthListResponse{Files: []pluginapi.HostAuthFileEntry{{
				Name:      oauthName,
				ID:        strings.TrimSuffix(oauthName, ".json"),
				AuthIndex: "idx-1",
			}}}), nil
		case pluginabi.MethodHostAuthGet:
			var req pluginapi.HostAuthGetRequest
			_ = json.Unmarshal(payload, &req)
			if req.AuthIndex != "idx-1" {
				return hostErr("auth_not_found", req.AuthIndex), nil
			}
			return hostOK(pluginapi.HostAuthGetResponse{
				AuthIndex: "idx-1", Name: oauthName, JSON: json.RawMessage(oauthJSON),
			}), nil
		default:
			return hostErr("not_implemented", method), nil
		}
	}
}

func TestCatalogFetchBearerPrefersAPIKey(t *testing.T) {
	bridge := NewHostBridge(oauthHostFake(
		oauthManagedPrefix+"deadbeef.json",
		`{"credential_type":"oauth","access_token":"st_oauth"}`, true))
	cfg := config.Config{APIKeys: []config.APIKey{{Value: "sk-api-key"}}}
	if got := catalogFetchBearer(context.Background(), cfg, bridge); got != "sk-api-key" {
		t.Fatalf("bearer = %q, want the configured API key to win", got)
	}
}

func TestCatalogFetchBearerFallsBackToOAuth(t *testing.T) {
	bridge := NewHostBridge(oauthHostFake(
		oauthManagedPrefix+"deadbeef.json",
		`{"credential_type":"oauth","access_token":"st_oauth_token"}`, true))
	cfg := config.Config{} // no api-keys: OAuth-only configuration
	if got := catalogFetchBearer(context.Background(), cfg, bridge); got != "st_oauth_token" {
		t.Fatalf("bearer = %q, want the OAuth access token", got)
	}
}

func TestCatalogFetchBearerSkipsNonOAuthRecords(t *testing.T) {
	// A managed API-key record must not be mistaken for an OAuth credential.
	bridge := NewHostBridge(oauthHostFake(
		"opencode-go-key-cafebabe.json",
		`{"type":"opencode-go","api_key":"sk-should-not-win"}`, true))
	cfg := config.Config{}
	if got := catalogFetchBearer(context.Background(), cfg, bridge); got != "" {
		t.Fatalf("bearer = %q, want empty (only non-OAuth records present)", got)
	}
}

func TestCatalogFetchBearerEmptyWhenNoAuthDir(t *testing.T) {
	bridge := NewHostBridge(oauthHostFake("x", "{}", false))
	cfg := config.Config{}
	if got := catalogFetchBearer(context.Background(), cfg, bridge); got != "" {
		t.Fatalf("bearer = %q, want empty when the auth dir is unavailable", got)
	}
}

func TestCatalogFetchBearerNilBridge(t *testing.T) {
	cfg := config.Config{}
	if got := catalogFetchBearer(context.Background(), cfg, nil); got != "" {
		t.Fatalf("bearer = %q, want empty with a nil bridge", got)
	}
}
