//go:build linux && cgo

package loadsmoke

// Offline load dry-run of the REAL artifact: the c-shared .so is dlopen'ed,
// cliproxy_plugin_init is called with a fake host ABI whose callbacks are
// answered in memory, and the plugin is driven through cliproxyPluginCall
// exactly as EasyCLI drives it.
//
// No EasyCLI binary, deploy host, network or credential is involved. Beyond
// identity and config reconcile, the assertions that matter are:
//   - the plugin publishes its pinned identity (`opencode-go-provider`,
//     2.3.0), its plan-aware config surface and native capabilities;
//   - a configured key is materialized into the host's credential store, and
//     a reconfigure observes the same record through the management list/get
//     path without rewriting it;
//   - credential bytes cross the ABI only on host.auth.save (the host's own
//     store write), host.auth.get (the host returning what it was given) and
//     the authenticated upstream catalog call; every other callback
//     payload/response, every plugin RPC response and every log line stays
//     credential-free.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	providerID = "opencode-go"
	pluginID   = "opencode-go-provider"
)

type hostCall struct {
	Method   string
	Request  []byte
	Response []byte
}

// smokeHost is the fake EasyCLI host: an in-memory credential store plus the
// callback answers the plugin needs during registration/reconfigure.
type smokeHost struct {
	mu      sync.Mutex
	dir     string
	records map[string][]byte
	calls   []hostCall
	logs    []string
}

func hostOK(result any) []byte {
	raw, _ := json.Marshal(result)
	out, _ := json.Marshal(pluginabi.Envelope{OK: true, Result: raw})
	return out
}

func hostErr(code, message string) []byte {
	out, _ := json.Marshal(pluginabi.Envelope{OK: false, Error: &pluginabi.Error{Code: code, Message: message}})
	return out
}

func (h *smokeHost) call(method string, payload []byte) ([]byte, error) {
	var raw []byte
	switch method {
	case pluginabi.MethodHostAuthList:
		h.mu.Lock()
		files := make([]pluginapi.HostAuthFileEntry, 0, len(h.records))
		for name, record := range h.records {
			var fields struct {
				Type     string `json:"type"`
				Disabled bool   `json:"disabled"`
			}
			_ = json.Unmarshal(record, &fields)
			files = append(files, pluginapi.HostAuthFileEntry{
				ID: strings.TrimSuffix(name, ".json"), AuthIndex: "auth-index-" + name,
				Name: name, Type: fields.Type, Provider: fields.Type, Disabled: fields.Disabled,
				Source: "file", Path: filepath.Join(h.dir, name),
			})
		}
		h.mu.Unlock()
		raw = hostOK(struct {
			Files []pluginapi.HostAuthFileEntry `json:"files"`
		}{Files: files})
	case pluginabi.MethodHostAuthGet:
		var req pluginapi.HostAuthGetRequest
		_ = json.Unmarshal(payload, &req)
		name := strings.TrimPrefix(req.AuthIndex, "auth-index-")
		h.mu.Lock()
		record, ok := h.records[name]
		h.mu.Unlock()
		if !ok {
			raw = hostErr("not_found", "auth file not found")
			break
		}
		raw = hostOK(pluginapi.HostAuthGetResponse{
			AuthIndex: req.AuthIndex, Name: name, Path: filepath.Join(h.dir, name), JSON: record,
		})
	case pluginabi.MethodHostAuthSave:
		var req pluginapi.HostAuthSaveRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			raw = hostErr("invalid_request", "bad auth save")
			break
		}
		path := filepath.Join(h.dir, req.Name)
		if err := os.WriteFile(path, req.JSON, 0o600); err != nil {
			raw = hostErr("auth_save_failed", "cannot write auth file")
			break
		}
		h.mu.Lock()
		h.records[req.Name] = append([]byte(nil), req.JSON...)
		h.mu.Unlock()
		raw = hostOK(pluginapi.HostAuthSaveResponse{Name: req.Name, Path: path})
	case pluginabi.MethodHostLog:
		var entry struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(payload, &entry)
		h.mu.Lock()
		h.logs = append(h.logs, entry.Message)
		h.mu.Unlock()
		raw = hostOK(map[string]any{})
	case pluginabi.MethodHostHTTPDo:
		// Catalog discovery only: one model, no network.
		raw = hostOK(pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte(`{"data":[{"id":"glm-5.3"}]}`),
		})
	default:
		raw = hostOK(map[string]any{})
	}
	h.mu.Lock()
	h.calls = append(h.calls, hostCall{Method: method, Request: payload, Response: raw})
	h.mu.Unlock()
	return raw, nil
}

func (h *smokeHost) snapshot() []hostCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hostCall(nil), h.calls...)
}

func (h *smokeHost) logLines() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.logs...)
}

func (h *smokeHost) storedRecord(name string) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]byte(nil), h.records[name]...)
}

func authRecordName(key string) string {
	digest := sha256.Sum256([]byte(key))
	return providerID + "-key-" + hex.EncodeToString(digest[:]) + ".json"
}

func pluginPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "plugins", "linux", "amd64", "easycliproxyapi-opencode-provider.so"))
	if err != nil {
		t.Fatalf("resolve plugin path: %v", err)
	}
	return path
}

func TestLoadDryRunRealPluginArtifact(t *testing.T) {
	path := pluginPath(t)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("plugin artifact not built (%s); CI builds it before go test", path)
	}

	host := &smokeHost{dir: t.TempDir(), records: map[string][]byte{}}
	SetHost(host.call)
	t.Cleanup(func() { SetHost(nil) })

	const key = "sk-loadsmoke-credential"
	loader, err := Open(path)
	if err != nil {
		t.Fatalf("open artifact: %v", err)
	}
	t.Cleanup(loader.Close)
	if err := loader.Init(); err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	if got := loader.ABI(); got != int(pluginabi.ABIVersion) {
		t.Fatalf("plugin ABI version = %d, want %d", got, pluginabi.ABIVersion)
	}

	configYAML := fmt.Sprintf(`api-keys:
  - value: %s
plans:
  - name: go
    base-url: https://upstream.invalid/zen/go/v1
    model-prefix:
      enabled: true
      value: %s
    enabled: true
catalog:
  refresh-interval: 15m
`, key, providerID)
	registerRequest, _ := json.Marshal(map[string][]byte{"config_yaml": []byte(configYAML)})

	var audit [][]byte
	call := func(method string, request []byte) pluginabi.Envelope {
		t.Helper()
		raw, err := loader.RPC(method, request)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		audit = append(audit, raw)
		var env pluginabi.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("%s: undecodable envelope %s", method, raw)
		}
		return env
	}

	// --- registration identity ------------------------------------------
	var registration struct {
		SchemaVersion uint32             `json:"schema_version"`
		Metadata      pluginapi.Metadata `json:"metadata"`
		Capabilities  struct {
			ModelProvider      bool     `json:"model_provider"`
			AuthProvider       bool     `json:"auth_provider"`
			Executor           bool     `json:"executor"`
			QuotaProvider      bool     `json:"quota_provider"`
			ExecutorModelScope string   `json:"executor_model_scope"`
			InputFormats       []string `json:"executor_input_formats"`
			OutputFormats      []string `json:"executor_output_formats"`
		} `json:"capabilities"`
	}
	env := call(pluginabi.MethodPluginRegister, registerRequest)
	if !env.OK {
		t.Fatalf("plugin.register rejected by the loaded plugin: %s", env.Result)
	}
	if err := json.Unmarshal(env.Result, &registration); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	if registration.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", registration.SchemaVersion, pluginabi.SchemaVersion)
	}
	if registration.Metadata.Name != pluginID ||
		registration.Metadata.Version != "2.3.0" ||
		registration.Metadata.GitHubRepository != "https://github.com/simple-central-ai-things/easycliproxyapi-opencode-plugin" {
		t.Fatalf("identity = %+v", registration.Metadata)
	}
	if !registration.Capabilities.ModelProvider || !registration.Capabilities.AuthProvider ||
		!registration.Capabilities.Executor || !registration.Capabilities.QuotaProvider {
		t.Fatalf("capabilities = %+v", registration.Capabilities)
	}
	if registration.Capabilities.ExecutorModelScope != string(pluginapi.ExecutorModelScopeOAuth) {
		t.Fatalf("executor_model_scope = %q", registration.Capabilities.ExecutorModelScope)
	}
	if strings.Join(registration.Capabilities.InputFormats, ",") != "openai,claude,openai-response" ||
		strings.Join(registration.Capabilities.OutputFormats, ",") != "openai,claude,openai-response" {
		t.Fatalf("executor formats in=%v out=%v", registration.Capabilities.InputFormats, registration.Capabilities.OutputFormats)
	}
	fields := map[string]pluginapi.ConfigFieldType{}
	for _, field := range registration.Metadata.ConfigFields {
		fields[field.Name] = field.Type
	}
	if fields["api-keys"] != pluginapi.ConfigFieldTypeArray || fields["plans"] != pluginapi.ConfigFieldTypeArray {
		t.Fatalf("config fields = %+v, want the plan schema", fields)
	}

	// --- config reconcile landed in the host credential store ------------
	name := authRecordName(key)
	record := host.storedRecord(name)
	if len(record) == 0 {
		t.Fatalf("reconciled credential %s missing from the host store", name)
	}
	var storedFields map[string]any
	if err := json.Unmarshal(record, &storedFields); err != nil {
		t.Fatalf("decode reconciled record: %v", err)
	}
	if storedFields["type"] != providerID || storedFields["api_key"] != key {
		t.Fatalf("reconciled record = %v", storedFields)
	}
	if _, disabled := storedFields["disabled"]; disabled {
		t.Fatal("reconciled record was written disabled")
	}

	// --- reconfigure is idempotent through the management list/get path --
	env = call(pluginabi.MethodPluginReconfigure, registerRequest)
	if !env.OK {
		t.Fatalf("plugin.reconfigure rejected by the loaded plugin: %s", env.Result)
	}
	if got := host.storedRecord(name); !bytes.Equal(got, record) {
		t.Fatalf("reconfigure rewrote the reconciled record: %s", got)
	}

	// --- model discovery, quota surface, typed unsupported paths ---------
	var models pluginapi.ModelResponse
	env = call(pluginabi.MethodModelStatic, nil)
	if !env.OK || json.Unmarshal(env.Result, &models) != nil || models.Provider != providerID {
		t.Fatalf("model.static = %s", env.Result)
	}
	ids := map[string]bool{}
	for _, model := range models.Models {
		ids[model.ID] = true
	}
	if !ids["opencode-go/glm-5.3"] {
		t.Fatalf("model.static published %v, want the configured plan prefix", ids)
	}

	var quota pluginapi.QuotaDescribeResponse
	env = call(pluginabi.MethodQuotaDescribe, []byte(`{}`))
	if !env.OK || json.Unmarshal(env.Result, &quota) != nil ||
		len(quota.SupportedProviders) != 1 || quota.SupportedProviders[0] != providerID || quota.SupportsReset {
		t.Fatalf("quota.describe = %s", env.Result)
	}

	env = call(pluginabi.MethodQuotaReset, nil)
	if env.OK || env.Error == nil || env.Error.Code != "unsupported_protocol_or_parameter" {
		t.Fatalf("quota.reset not refused as unsupported: %s", env.Result)
	}

	env = call(pluginabi.MethodPluginShutdown, nil)
	if !env.OK {
		t.Fatalf("plugin.shutdown failed: %s", env.Result)
	}
	loader.Shutdown()

	// --- no-data-leak smoke across the ABI ------------------------------
	secrets := []string{key, base64.StdEncoding.EncodeToString([]byte(key))}
	assertClean := func(where string, raw []byte) {
		t.Helper()
		for _, secret := range secrets {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("%s leaked credential bytes: %s", where, raw)
			}
		}
	}
	for _, captured := range host.snapshot() {
		switch captured.Method {
		case pluginabi.MethodHostAuthSave:
			// host.auth.save is the host's credential store: the credential is
			// supposed to cross there and nowhere else.
			continue
		case pluginabi.MethodHostAuthGet:
			// host.auth.get returns the record the HOST itself stored. Allow
			// exactly that host-supplied material and nothing more.
			assertClean("host.auth.get request", captured.Request)
			var response pluginapi.HostAuthGetResponse
			var envelope pluginabi.Envelope
			if err := json.Unmarshal(captured.Response, &envelope); err != nil || !envelope.OK {
				t.Fatalf("host.auth.get response envelope = %s", captured.Response)
			}
			if err := json.Unmarshal(envelope.Result, &response); err != nil {
				t.Fatalf("host.auth.get response = %s", captured.Response)
			}
			if !bytes.Equal(response.JSON, record) {
				t.Fatalf("host.auth.get returned credential material the host did not supply: %s", response.JSON)
			}
			continue
		case pluginabi.MethodHostHTTPDo:
			// host.http.do is the host's outbound transport: the plugin must
			// authenticate the catalog call. Assert it does, then treat it as
			// the intended (non-management) channel.
			if !strings.Contains(string(captured.Request), "Bearer "+key) {
				t.Fatalf("upstream call did not authenticate with the configured key: %s", captured.Request)
			}
			continue
		}
		assertClean(captured.Method+" request", captured.Request)
		assertClean(captured.Method+" response", captured.Response)
	}
	for _, line := range host.logLines() {
		assertClean("host log", []byte(line))
	}
	for _, raw := range audit {
		assertClean("plugin RPC response", raw)
	}
}
