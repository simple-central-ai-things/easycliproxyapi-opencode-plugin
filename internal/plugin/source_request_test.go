package plugin

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"opencode-go-cliproxyapi/internal/adapter/shared"
	"opencode-go-cliproxyapi/internal/catalog"
	"opencode-go-cliproxyapi/internal/errclass"
)

// TestExecutorPreparationSharesOneSourceRequest drives the exact helper
// sequence both execution paths use — session derivation, then conversion —
// against ONE SourceRequest and checks each result against the legacy
// single-purpose helpers. The shared decode cache is what makes the second
// step reuse the first step's parse instead of repeating it (see the
// cache-level tests in internal/adapter/shared).
func TestExecutorPreparationSharesOneSourceRequest(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":"same"}]}`)
	req := executorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		SourceFormat: "openai-response", OriginalRequest: body,
	}}
	source := shared.NewSourceRequest(req.SourceFormat, req.OriginalRequest)

	gotSession, eErr := resolveOpenCodeSessionIDFrom(req, source)
	if eErr != nil {
		t.Fatalf("session: %v", eErr)
	}
	wantSession, eErr := deriveOpenCodeSessionID("openai-response", body)
	if eErr != nil || gotSession != wantSession {
		t.Fatalf("session = %q (%v), want %q", gotSession, eErr, wantSession)
	}

	gotBody, eErr := buildUpstreamRequestFrom(catalog.RouteChatCompletions, "deepseek-v4-pro", "", source, nil)
	if eErr != nil {
		t.Fatalf("build: %v", eErr)
	}
	wantBody, eErr := buildUpstreamRequest(catalog.RouteChatCompletions, "deepseek-v4-pro", "openai-response", body, nil)
	if eErr != nil {
		t.Fatalf("legacy build: %v", eErr)
	}
	if !bytes.Equal(gotBody, wantBody) {
		t.Fatalf("shared conversion differs from legacy:\n got: %s\nwant: %s", gotBody, wantBody)
	}
	if !bytes.Equal(source.Raw(), body) {
		t.Fatalf("Raw altered: %s", source.Raw())
	}
}

// TestExecutorNativePassthroughWithSessionIdentitySkipsTypedDecode is the
// passthrough guard: a native Responses body carrying a shape the typed
// conversion cannot model round-trips byte-exact when session identity is
// supplied, and still fails with the same translation error when the fallback
// content digest has to decode it. That is only true if the native branch uses
// the raw body and the metadata/header short-circuit keeps the typed decode
// off the request path.
//
// Both lazy decodes are covered, not only the input-item one: an input-item
// failure still parses as a typed envelope (Input is json.RawMessage), so a
// native path that accidentally decoded the envelope would slip past the first
// assertNativeInputPreserved verifies the responses "input" array survives
// native passthrough verbatim, allowing for the leading env system message the
// free-tier gate injection prepends.
func assertNativeInputPreserved(t *testing.T, got, want []byte) {
	t.Helper()
	var gotDoc, wantDoc map[string]json.RawMessage
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("decode got: %v", err)
	}
	if err := json.Unmarshal(want, &wantDoc); err != nil {
		t.Fatalf("decode want: %v", err)
	}
	var gotInput []json.RawMessage
	if err := json.Unmarshal(gotDoc["input"], &gotInput); err != nil {
		t.Fatalf("decode got input: %v", err)
	}
	if len(gotInput) > 0 {
		var first map[string]any
		if err := json.Unmarshal(gotInput[0], &first); err == nil {
			if role, _ := first["role"].(string); role == "system" {
				gotInput = gotInput[1:]
			}
		}
	}
	gotInputJSON, _ := json.Marshal(gotInput)
	wantInputJSON, _ := json.Marshal(wantDoc["input"])
	if !bytes.Equal(gotInputJSON, wantInputJSON) {
		t.Fatalf("native input not preserved:\n got: %s\nwant: %s", gotInputJSON, wantInputJSON)
	}
}

// case. The second case defeats the envelope decode itself.
func TestExecutorNativePassthroughWithSessionIdentitySkipsTypedDecode(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"input-items-undecodable", `{"model":"gpt-5.6-luna","input":["raw-string-entry"]}`},
		// max_output_tokens is typed *int64, so the typed envelope cannot
		// decode a string here while native passthrough forwards it verbatim.
		{"envelope-undecodable", `{"model":"gpt-5.6-luna","max_output_tokens":"opaque","input":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)

			m, f := newExecManager(t)
			resp, err := m.HandleCall("executor.execute",
				execReqBodyWithSessionInputs("gpt-5.6-luna", "openai-response", body, false, testKey, "canonical-native", ""))
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if env := decodeEnv(t, resp); !env.OK {
				t.Fatalf("passthrough with identity failed: %+v", env.Error)
			}
			wire := lastWire(t, f, pluginabi.MethodHostHTTPDo)
			got := wireBody(t, wire, "body")
			// The env block is prepended as a leading input system message for
			// the free-tier gate; the native passthrough content itself must
			// survive verbatim. Strip the leading system message and compare.
			assertNativeInputPreserved(t, got, body)
			assertSessionHeaders(t, wire, "canonical-native", "Bearer "+testKey)

			m2, _ := newExecManager(t)
			resp2, err := m2.HandleCall("executor.execute",
				execReqBodyWithSessionInputs("gpt-5.6-luna", "openai-response", body, false, testKey, "", ""))
			if err != nil {
				t.Fatalf("execute without identity: %v", err)
			}
			env2 := decodeEnv(t, resp2)
			if env2.OK || env2.Error == nil || env2.Error.Code != string(errclass.ClassTranslation) {
				t.Fatalf("untyped input without identity must stay a translation failure: %+v", env2.Error)
			}
		})
	}
}

// TestExecutorSessionIdentityMatrix extends the format x route x mode matrix
// with the two short-circuit identity sources — CPA's canonical metadata and
// an inbound session header — proving they win before the content digest on
// every leg, in both execution modes.
func TestExecutorSessionIdentityMatrix(t *testing.T) {
	formats := []struct {
		name string
		body []byte
	}{
		{"openai", []byte(`{"model":"x","messages":[{"role":"user","content":"matrix"}]}`)},
		{"claude", []byte(`{"model":"x","max_tokens":16,"messages":[{"role":"user","content":"matrix"}]}`)},
		{"openai-response", []byte(`{"model":"x","input":"matrix"}`)},
	}
	routes := []struct {
		name  string
		model string
		auth  string
	}{
		{"chat", "opencode-go/glm-5.3", "Bearer " + testKey},
		{"messages", "opencode-go/minimax-m3", ""},
		{"responses", "opencode-go/gpt-5.6-luna", "Bearer " + testKey},
	}
	identities := []struct {
		name      string
		canonical string
		header    string
		want      string
	}{
		{"canonical", "canonical-identity", "header-identity", "header-identity"},
		{"header", "", "header-identity", "header-identity"},
	}
	for _, mode := range []string{"non-stream", "stream"} {
		for _, format := range formats {
			for _, route := range routes {
				for _, id := range identities {
					t.Run(mode+"/"+format.name+"/"+route.name+"/"+id.name, func(t *testing.T) {
						if mode == "non-stream" {
							m, f := newExecManager(t)
							resp, err := m.HandleCall("executor.execute", execReqBodyWithSessionInputs(route.model, format.name, format.body, false, testKey, id.canonical, id.header))
							if err != nil || !decodeEnv(t, resp).OK {
								t.Fatalf("execute: %v %s", err, resp)
							}
							assertSessionHeaders(t, lastWire(t, f, pluginabi.MethodHostHTTPDo), id.want, route.auth)
							return
						}
						m, f := newStreamManager(t, streamScript{upstreamID: "identity-up"})
						resp, err := m.HandleCall("executor.execute_stream", execReqBodyWithSessionInputs(route.model, format.name, format.body, true, testKey, id.canonical, id.header))
						if err != nil || !decodeEnv(t, resp).OK {
							t.Fatalf("execute_stream: %v %s", err, resp)
						}
						calls := f.callsOf(pluginabi.MethodHostHTTPDoStream)
						if len(calls) != 1 {
							t.Fatalf("do_stream calls = %d, want 1", len(calls))
						}
						assertSessionHeaders(t, decodePayload(t, calls[0]), id.want, route.auth)
					})
				}
			}
		}
	}
}
