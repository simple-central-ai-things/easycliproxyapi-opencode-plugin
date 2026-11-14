package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// This file pins the binary/ABI surface the released plugin speaks (v2.5.0, built on the v2.3.0 ABI):
// the CLIProxyAPI v8 native ABI version, RPC schema version, registration
// identity/capabilities, quota-provider card surface and the plan-aware
// dispatch. Everything here runs offline against synthetic host callbacks.

func TestABIContractVersionPins(t *testing.T) {
	// cliproxy_plugin_init rejects a host whose abi_version differs, so a
	// dependency downgrade must fail this test rather than a deploy.
	if pluginabi.ABIVersion != 1 {
		t.Fatalf("ABIVersion = %d, want 1 (native C ABI shape)", pluginabi.ABIVersion)
	}
	if pluginabi.SchemaVersion != 6 {
		t.Fatalf("SchemaVersion = %d, want 6 (v8 RPC schema: raw management responses)", pluginabi.SchemaVersion)
	}
	if pluginabi.SchemaVersionRawManagementResponse != 6 {
		t.Fatalf("SchemaVersionRawManagementResponse = %d, want 6", pluginabi.SchemaVersionRawManagementResponse)
	}
}

// deployedABIYAML is the plan schema the EasyCLI config contract uses, with
// synthetic hosts and keys.
const deployedABIYAML = `api-keys:
  - value: sk-abi-key-1
plans:
  - name: go
    base-url: https://go.abi.test/zen/go/v1
    model-prefix:
      enabled: true
      value: opencode-go
    enabled: true
  - name: zen
    base-url: https://zen.abi.test/zen/v1
    model-prefix:
      enabled: true
      value: opencode-zen
    enabled: true
catalog:
  refresh-interval: 15m
`

// planCatalogResponder answers each plan's catalog URL with its own models.
func planCatalogResponder(t *testing.T, byHost map[string]string) func(string, []byte) ([]byte, error) {
	t.Helper()
	return func(method string, payload []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		var wire struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(payload, &wire); err != nil {
			t.Fatalf("bad host.http.do payload: %s", payload)
		}
		for host, body := range byHost {
			if strings.HasPrefix(wire.URL, "https://"+host+"/") {
				return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(body)}), nil
			}
		}
		return hostErr("unexpected_url", wire.URL), nil
	}
}

func TestABIRegistrationIdentityAndCapabilities(t *testing.T) {
	m, _ := newTestManager(planCatalogResponder(t, map[string]string{}))
	t.Cleanup(func() { _, _ = m.HandleCall(pluginabi.MethodPluginShutdown, nil) })
	var reg registrationResult
	decodeResult(t, mustHandle(t, m, pluginabi.MethodPluginRegister, lifecycleRequestBody(deployedABIYAML)), &reg)

	if reg.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", reg.SchemaVersion, pluginabi.SchemaVersion)
	}
	if reg.Metadata.Name != "easycliproxyapi-opencode-provider" {
		t.Fatalf("plugin id = %q, want the SCPM config-contract identity", reg.Metadata.Name)
	}
	if reg.Metadata.Version != "2.5.1" {
		t.Fatalf("version = %q, want 2.5.1 (released artifact)", reg.Metadata.Version)
	}
	if reg.Metadata.GitHubRepository != "https://github.com/simple-central-ai-things/easycliproxyapi-opencode-plugin" {
		t.Fatalf("github repository = %q", reg.Metadata.GitHubRepository)
	}
	caps := reg.Capabilities
	if !caps.ModelProvider || !caps.AuthProvider || !caps.Executor || !caps.QuotaProvider {
		t.Fatalf("capabilities = %+v", caps)
	}
	if caps.ExecutorModelScope != pluginapi.ExecutorModelScopeOAuth {
		t.Fatalf("executor_model_scope = %q", caps.ExecutorModelScope)
	}
	raw, _ := json.Marshal(caps)
	if strings.Contains(string(raw), "management_api") || strings.Contains(string(raw), "request_translator") {
		t.Fatalf("the release must advertise only provider/auth/executor/quota: %s", raw)
	}
	// Config-field surface must expose the plan schema, not just base-url.
	fields := map[string]pluginapi.ConfigFieldType{}
	for _, f := range reg.Metadata.ConfigFields {
		fields[f.Name] = f.Type
	}
	if fields["plans"] != pluginapi.ConfigFieldTypeArray {
		t.Fatalf("plans config field = %+v", fields)
	}
	if fields["api-keys"] != pluginapi.ConfigFieldTypeArray {
		t.Fatalf("api-keys config field = %+v", fields)
	}
}

func TestABIPlanSchemaPublishesPerPlanModels(t *testing.T) {
	m, _ := newTestManager(planCatalogResponder(t, map[string]string{
		"go.abi.test":  `{"data":[{"id":"glm-5.3"}]}`,
		"zen.abi.test": `{"data":[{"id":"qwen3.7-max"}]}`,
	}))
	t.Cleanup(func() { _, _ = m.HandleCall(pluginabi.MethodPluginShutdown, nil) })
	if _, err := m.HandleCall(pluginabi.MethodPluginRegister, lifecycleRequestBody(deployedABIYAML)); err != nil {
		t.Fatalf("register: %v", err)
	}
	var models pluginapi.ModelResponse
	decodeResult(t, mustHandle(t, m, pluginabi.MethodModelStatic, nil), &models)
	if models.Provider != ProviderID {
		t.Fatalf("provider = %q", models.Provider)
	}
	ids := map[string]bool{}
	for _, model := range models.Models {
		ids[model.ID] = true
	}
	if !ids["opencode-go/glm-5.3"] || !ids["opencode-zen/qwen3.7-max"] {
		t.Fatalf("published models = %v, want one per plan prefix", ids)
	}
}

func TestABIMethodSurface(t *testing.T) {
	m := NewManager(nil)
	t.Cleanup(func() { _, _ = m.HandleCall(pluginabi.MethodPluginShutdown, nil) })
	// Every v8 method this plugin claims must answer instead of falling
	// through to unknown_method.
	claimed := []string{
		pluginabi.MethodPluginRegister,
		pluginabi.MethodPluginReconfigure,
		pluginabi.MethodModelStatic,
		pluginabi.MethodModelForAuth,
		pluginabi.MethodExecutorIdentifier,
		pluginabi.MethodAuthIdentifier,
		pluginabi.MethodAuthParse,
		pluginabi.MethodAuthRefresh,
		pluginabi.MethodQuotaIdentifier,
		pluginabi.MethodQuotaDescribe,
		pluginabi.MethodQuotaFetch,
		pluginabi.MethodQuotaReset,
		pluginabi.MethodExecutorCountTokens,
		pluginabi.MethodExecutorHTTPRequest,
	}
	var forAuth pluginapi.AuthModelRequest
	forAuth.AuthProvider = ProviderID
	forAuthBody, _ := json.Marshal(forAuth)
	payloads := map[string][]byte{
		pluginabi.MethodModelForAuth: forAuthBody,
	}
	for _, method := range claimed {
		resp, err := m.HandleCall(method, payloads[method])
		if err != nil {
			t.Fatalf("%s: transport error %v", method, err)
		}
		var env pluginabi.Envelope
		if err := json.Unmarshal(resp, &env); err != nil {
			t.Fatalf("%s: invalid envelope %s", method, resp)
		}
		if env.Error != nil && env.Error.Code == "unknown_method" {
			t.Fatalf("%s is not dispatched", method)
		}
	}

	resp, err := m.HandleCall("not.a.method", nil)
	if err != nil {
		t.Fatal(err)
	}
	env := decodeEnv(t, resp)
	if env.OK || env.Error == nil || env.Error.Code != "unknown_method" {
		t.Fatalf("unknown method envelope = %s", resp)
	}
}

func TestABIAuthRecordsRoundTrip(t *testing.T) {
	// The record the reconciler materializes must parse back through the
	// auth-provider RPC surface with the same id and api-key attribute.
	const key = "sk-abi-auth-key"
	id := authRecordID(key)
	if id != "opencode-go-key-"+mustHashPrefix(key) {
		t.Fatalf("auth record id = %q", id)
	}
	record, err := json.Marshal(struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		Label  string `json:"label"`
		APIKey string `json:"api_key"`
	}{Type: ProviderID, ID: id, Label: "OpenCode Go credential", APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := json.Marshal(pluginapi.AuthParseRequest{Provider: ProviderID, FileName: id + ".json", RawJSON: record})
	m := NewManager(nil)
	var parsed pluginapi.AuthParseResponse
	decodeResult(t, mustHandle(t, m, pluginabi.MethodAuthParse, req), &parsed)
	if !parsed.Handled || parsed.Auth.ID != id || parsed.Auth.Provider != ProviderID {
		t.Fatalf("parsed auth = %+v", parsed.Auth)
	}
	if parsed.Auth.Attributes["api_key"] != key {
		t.Fatalf("parsed attributes = %v", parsed.Auth.Attributes)
	}
	if strings.Contains(string(mustHandle(t, m, pluginabi.MethodAuthIdentifier, nil)), key) {
		t.Fatal("auth.identifier leaked the key")
	}
}

// TestABIAuthLoginWired checks the C-ABI dispatch reaches the login handler.
// With no host bridge (the loadsmoke manager) it reports auth_failure rather
// than the retired "unsupported", proving the method is implemented.
func TestABIAuthLoginWired(t *testing.T) {
	m := NewManager(nil)
	resp, err := m.HandleCall(pluginabi.MethodAuthLoginStart, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	env := decodeEnv(t, resp)
	if env.OK || env.Error == nil || env.Error.Code != "auth_failure" {
		t.Fatalf("auth login start envelope = %s", resp)
	}
}

func TestABIQuotaProviderSurface(t *testing.T) {
	m := NewManager(nil)
	var describe pluginapi.QuotaDescribeResponse
	decodeResult(t, mustHandle(t, m, pluginabi.MethodQuotaDescribe, []byte(`{}`)), &describe)
	if len(describe.SupportedProviders) != 1 || describe.SupportedProviders[0] != ProviderID || describe.SupportsReset {
		t.Fatalf("describe = %+v", describe)
	}
	var ident struct {
		Identifier string `json:"identifier"`
	}
	decodeResult(t, mustHandle(t, m, pluginabi.MethodQuotaIdentifier, nil), &ident)
	if ident.Identifier != ProviderID {
		t.Fatalf("quota identifier = %q", ident.Identifier)
	}
}
