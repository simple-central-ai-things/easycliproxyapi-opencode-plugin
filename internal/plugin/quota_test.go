package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"opencode-go-cliproxyapi/internal/config"
)

// mustHashPrefix returns the sha256 hex prefix the auth-record and quota
// identity use for a key, without exposing the key itself.
func mustHashPrefix(key string) string {
	id, _ := quotaIdentity(key)
	return strings.TrimPrefix(id, managedAuthPrefix)
}

// planConfig builds a two-plan config equivalent to the deployed template:// `go` enabled with prefix opencode-go, `zen` disabled with prefix
// opencode-zen. baseURLs stay synthetic (no live upstream).
func planConfig(apiKeys ...string) config.Config {
	keys := make([]config.APIKey, len(apiKeys))
	for i, k := range apiKeys {
		keys[i] = config.APIKey{Value: k}
	}
	return config.Config{
		APIKeys:        keys,
		RequestTimeout: config.DefaultRequestTimeout,
		Plans: []config.Plan{
			{Name: "go", BaseURL: "https://go.quota.test/v1", CatalogURL: "https://go.quota.test/v1/models", ModelPrefix: config.ModelPrefix{Enabled: true, Value: "opencode-go"}, Enabled: true},
			{Name: "zen", BaseURL: "https://zen.quota.test/v1", CatalogURL: "https://zen.quota.test/v1/models", ModelPrefix: config.ModelPrefix{Enabled: true, Value: "opencode-zen"}, Enabled: false},
		},
	}
}

func TestQuotaRegistrationAdvertisesNativeCardsOnly(t *testing.T) {
	m := NewManager(nil)
	var registration registrationResult
	decodeResult(t, mustHandle(t, m, pluginabi.MethodPluginRegister, lifecycleRequestBody(testValidYAML)), &registration)
	if !registration.Capabilities.QuotaProvider {
		t.Fatal("registration did not advertise quota_provider")
	}
	raw, _ := json.Marshal(registration.Capabilities)
	if strings.Contains(string(raw), "management_api") {
		t.Fatalf("v2.3.0 must not advertise a plugin-owned management page: %s", raw)
	}
}

func TestQuotaIdentifierDescribeAndReset(t *testing.T) {
	m := NewManager(nil)
	var ident struct {
		Identifier string `json:"identifier"`
	}
	decodeResult(t, mustHandle(t, m, pluginabi.MethodQuotaIdentifier, nil), &ident)
	if ident.Identifier != ProviderID {
		t.Fatalf("quota identifier = %q, want %q", ident.Identifier, ProviderID)
	}
	var describe pluginapi.QuotaDescribeResponse
	decodeResult(t, mustHandle(t, m, pluginabi.MethodQuotaDescribe, []byte(`{}`)), &describe)
	if describe.DisplayName != "OpenCode Go" || describe.SupportsReset || len(describe.SupportedProviders) != 1 || describe.SupportedProviders[0] != ProviderID {
		t.Fatalf("describe = %+v", describe)
	}
	reset, err := m.HandleCall(pluginabi.MethodQuotaReset, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var env pluginabi.Envelope
	if err := json.Unmarshal(reset, &env); err != nil || env.OK {
		t.Fatalf("quota.reset envelope = %s err=%v", reset, err)
	}
	if env.Error == nil || env.Error.Code != "unsupported_protocol_or_parameter" {
		t.Fatalf("quota.reset error = %+v", env.Error)
	}
}

func TestQuotaFetchCardSurface(t *testing.T) {
	const key = "quota-refresh-secret"
	var sawURL string
	f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		var wire struct {
			Method  string      `json:"method"`
			URL     string      `json:"url"`
			Headers http.Header `json:"headers"`
		}
		if err := json.Unmarshal(payload, &wire); err != nil || wire.Method != http.MethodGet || wire.Headers.Get("Authorization") != "Bearer "+key || wire.Headers.Get("Accept") != "application/json" {
			t.Fatalf("bad quota request: %s", payload)
		}
		sawURL = wire.URL
		return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"usage":{"rolling":{"status":"ok","percent":9,"resetsAt":"2026-09-05T12:20:04Z"},"weekly":{"status":"ok","percent":12,"resetsAt":"2026-09-10T00:00:00Z"},"monthly":{"status":"ok","percent":6,"resetsAt":"2026-10-01T00:00:00Z"}}}`)}), nil
	}}
	m := NewManager(NewHostBridge(f.call))
	m.cfg = planConfig(key)
	var card pluginapi.QuotaFetchResponse
	decodeResult(t, mustHandle(t, m, pluginabi.MethodQuotaFetch, []byte(`{"auth_id":"opencode-go-key-test","provider":"opencode-go","attributes":{"api_key":"`+key+`"}}`)), &card)
	if sawURL != "https://go.quota.test/v1/usage" {
		t.Fatalf("quota url = %q, want the first enabled plan", sawURL)
	}
	if card.Subscription == nil || card.Subscription.Plan != "go" {
		t.Fatalf("subscription = %+v", card.Subscription)
	}
	if len(card.Groups) != 1 || len(card.Groups[0].Buckets) != 3 {
		t.Fatalf("groups = %+v", card.Groups)
	}
	bucket := card.Groups[0].Buckets[0]
	if bucket.RemainingFraction != 0.91 || bucket.ResetTime != "2026-09-05T12:20:04Z" || bucket.Window != "ok" {
		t.Fatalf("rolling bucket = %+v", bucket)
	}
	if len(card.Summary) != 1 || card.Summary[0].Value != 94 {
		t.Fatalf("summary = %+v", card.Summary)
	}
}

func TestQuotaFetchHonorsPlanAttribute(t *testing.T) {
	const key = "quota-plan-secret"
	var sawURL string
	f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		var wire struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(payload, &wire)
		sawURL = wire.URL
		return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"usage":{}}`)}), nil
	}}
	m := NewManager(NewHostBridge(f.call))
	cfg := planConfig(key)
	// Enable the second plan so an explicit attribute can select it.
	cfg.Plans[1].Enabled = true
	m.cfg = cfg
	_, err := m.HandleCall(pluginabi.MethodQuotaFetch, []byte(`{"provider":"opencode-go","attributes":{"api_key":"`+key+`","plan":"zen"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if sawURL != "https://zen.quota.test/v1/usage" {
		t.Fatalf("quota url = %q, want the plan-attribute upstream", sawURL)
	}
}

func TestQuotaFetchRejectsMissingKeyAndForeignProvider(t *testing.T) {
	f := &fakeCaller{}
	m := NewManager(NewHostBridge(f.call))
	m.cfg = planConfig("quota-key")
	for _, body := range []string{
		`{"provider":"opencode-go","attributes":{}}`,
		`{"provider":"other","attributes":{"api_key":"quota-key"}}`,
	} {
		resp, err := m.HandleCall(pluginabi.MethodQuotaFetch, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		var env pluginabi.Envelope
		if err := json.Unmarshal(resp, &env); err != nil || env.OK || env.Error == nil || env.Error.Code != "quota_failure" {
			t.Fatalf("body=%s envelope=%s err=%v", body, resp, err)
		}
	}
	if len(f.callsOf(pluginabi.MethodHostHTTPDo)) != 0 {
		t.Fatalf("rejected fetch must not reach the upstream: %v", f.callsOf(pluginabi.MethodHostHTTPDo))
	}
}

func TestQuotaFetchRequiresEnabledPlan(t *testing.T) {
	f := &fakeCaller{}
	m := NewManager(NewHostBridge(f.call))
	cfg := planConfig("quota-key")
	cfg.Plans[0].Enabled = false
	m.cfg = cfg
	resp, err := m.HandleCall(pluginabi.MethodQuotaFetch, []byte(`{"provider":"opencode-go","attributes":{"api_key":"quota-key"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var env pluginabi.Envelope
	if err := json.Unmarshal(resp, &env); err != nil || env.OK || env.Error == nil || !strings.Contains(env.Error.Message, "no enabled") {
		t.Fatalf("envelope = %s err=%v", resp, err)
	}
}

func TestQuotaUpstreamFailureIsClassified(t *testing.T) {
	f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusBadGateway, Body: []byte(`{"error":"upstream"}`)}), nil
	}}
	m := NewManager(NewHostBridge(f.call))
	m.cfg = planConfig("quota-key")
	resp, err := m.HandleCall(pluginabi.MethodQuotaFetch, []byte(`{"provider":"opencode-go","attributes":{"api_key":"quota-key"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var env pluginabi.Envelope
	if err := json.Unmarshal(resp, &env); err != nil || env.OK || env.Error == nil || env.Error.Code != "quota_failure" {
		t.Fatalf("envelope = %s err=%v", resp, err)
	}
	if strings.Contains(string(resp), "quota-key") {
		t.Fatalf("quota failure leaked the credential: %s", resp)
	}
}

func TestQuotaProviderSatisfiesV8Interface(t *testing.T) {
	var provider pluginapi.QuotaProvider = quotaProvider{manager: NewManager(nil)}
	if provider.Identifier() != ProviderID {
		t.Fatalf("identifier = %q", provider.Identifier())
	}
}

func TestQuotaIdentityIsNonSecretAndStable(t *testing.T) {
	id, label := quotaIdentity("quota-key-a")
	hash := mustHashPrefix("quota-key-a")
	if id != "opencode-go-key-"+hash || label != "OpenCode Go credential "+hash[:12] {
		t.Fatalf("identity = %q %q", id, label)
	}
	if strings.Contains(id, "quota-key-a") || strings.Contains(label, "quota-key-a") {
		t.Fatal("quota identity leaked the key")
	}
}

func TestQuotaPlanResolution(t *testing.T) {
	cfg := planConfig("k")
	if plan, ok := quotaPlan(cfg, nil); !ok || plan.Name != "go" {
		t.Fatalf("default plan = %+v ok=%t", plan, ok)
	}
	if _, ok := quotaPlan(cfg, map[string]string{"plan": "zen"}); ok {
		t.Fatal("a disabled plan must not be resolvable for quota")
	}
	cfg.Plans[1].Enabled = true
	if plan, ok := quotaPlan(cfg, map[string]string{"plan": "zen"}); !ok || plan.BaseURL != "https://zen.quota.test/v1" {
		t.Fatalf("explicit plan = %+v ok=%t", plan, ok)
	}
	if _, ok := quotaPlan(cfg, map[string]string{"plan": "unknown"}); ok {
		t.Fatal("an unknown plan attribute must not fall back")
	}
}

// TestQuotaFetchUsesMetadataKeyFallback pins the sibling-plugin parity rule:
// host.auth.save exposes a freshly written credential through Metadata before
// the auth-directory watcher re-parses it into Attributes, so a card refresh
// in that window must still find the key.
func TestQuotaFetchUsesMetadataKeyFallback(t *testing.T) {
	const key = "quota-metadata-secret"
	f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		var wire struct {
			Headers http.Header `json:"headers"`
		}
		if err := json.Unmarshal(payload, &wire); err != nil || wire.Headers.Get("Authorization") != "Bearer "+key {
			t.Fatalf("bad quota request: %s", payload)
		}
		return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"usage":{"monthly":{"status":"ok","percent":40}}}`)}), nil
	}}
	m := NewManager(NewHostBridge(f.call))
	m.cfg = planConfig(key)
	var card pluginapi.QuotaFetchResponse
	decodeResult(t, mustHandle(t, m, pluginabi.MethodQuotaFetch, []byte(`{"auth_id":"opencode-go-key-test","provider":"opencode-go","metadata":{"api_key":"`+key+`"}}`)), &card)
	if card.Subscription == nil || card.Subscription.Plan != "go" {
		t.Fatalf("subscription = %+v", card.Subscription)
	}
	if len(card.Summary) != 1 || card.Summary[0].Value != 60 {
		t.Fatalf("summary = %+v", card.Summary)
	}
}

// TestQuotaRemainingFractionClampsToBarBounds ensures an upstream percent
// outside 0..100 cannot draw a bar past its own ends or report negative quota.
func TestQuotaRemainingFractionClampsToBarBounds(t *testing.T) {
	for _, tc := range []struct {
		percent int
		want    float64
	}{{-20, 1}, {0, 1}, {37, 0.63}, {100, 0}, {140, 0}} {
		if got := quotaBucketFromWindow(quotaWindow{Status: "ok", Percent: tc.percent}, "window").RemainingFraction; got != tc.want {
			t.Fatalf("percent %d -> fraction %v, want %v", tc.percent, got, tc.want)
		}
		if got := quotaRemainingPercent(tc.percent); got != tc.want*100 {
			t.Fatalf("percent %d -> remaining %v, want %v", tc.percent, got, tc.want*100)
		}
	}
}

// TestQuotaFetchOmitsUnreportedWindows keeps the card honest: a window the
// upstream never described is omitted instead of being drawn as a full bar.
func TestQuotaFetchOmitsUnreportedWindows(t *testing.T) {
	f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"usage":{"weekly":{"status":"ok","percent":25}}}`)}), nil
	}}
	m := NewManager(NewHostBridge(f.call))
	m.cfg = planConfig("quota-key")
	var card pluginapi.QuotaFetchResponse
	decodeResult(t, mustHandle(t, m, pluginabi.MethodQuotaFetch, []byte(`{"provider":"opencode-go","attributes":{"api_key":"quota-key"}}`)), &card)
	if card.Subscription == nil || card.Subscription.Plan != "go" {
		t.Fatalf("subscription = %+v", card.Subscription)
	}
	if len(card.Summary) != 0 {
		t.Fatalf("summary = %+v, want none without a monthly window", card.Summary)
	}
	if len(card.Groups) != 1 || len(card.Groups[0].Buckets) != 1 {
		t.Fatalf("groups = %+v, want only the reported window", card.Groups)
	}
	if bucket := card.Groups[0].Buckets[0]; bucket.RemainingFraction != 0.75 || bucket.Description != "Weekly usage window" {
		t.Fatalf("weekly bucket = %+v", bucket)
	}
}

// TestQuotaFetchWithNoWindowsOmitsGroups covers an upstream that answers but
// reports nothing: the card keeps its plan label and claims no capacity.
func TestQuotaFetchWithNoWindowsOmitsGroups(t *testing.T) {
	f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"usage":{}}`)}), nil
	}}
	m := NewManager(NewHostBridge(f.call))
	m.cfg = planConfig("quota-key")
	var card pluginapi.QuotaFetchResponse
	decodeResult(t, mustHandle(t, m, pluginabi.MethodQuotaFetch, []byte(`{"provider":"opencode-go","attributes":{"api_key":"quota-key"}}`)), &card)
	if card.Subscription == nil || card.Subscription.Plan != "go" {
		t.Fatalf("subscription = %+v", card.Subscription)
	}
	if len(card.Groups) != 0 || len(card.Summary) != 0 {
		t.Fatalf("card = %+v, want plan label only", card)
	}
}
