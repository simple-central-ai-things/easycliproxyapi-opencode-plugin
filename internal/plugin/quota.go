package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"opencode-go-cliproxyapi/internal/config"
)

// quotaMaxTimeout bounds one quota-card refresh. It is independent of the
// catalog/completion request-timeout: a card refresh is an operator-driven
// read, so it must not inherit a long upstream completion budget.
const quotaMaxTimeout = 20 * time.Second

type quotaWindow struct {
	Status   string `json:"status"`
	Percent  int    `json:"percent"`
	ResetsAt string `json:"resets_at"`
}

type quotaUsage struct {
	Rolling quotaWindow `json:"rolling"`
	Weekly  quotaWindow `json:"weekly"`
	Monthly quotaWindow `json:"monthly"`
}

type usageResponse struct {
	Usage struct {
		Rolling quotaUpstreamWindow `json:"rolling"`
		Weekly  quotaUpstreamWindow `json:"weekly"`
		Monthly quotaUpstreamWindow `json:"monthly"`
	} `json:"usage"`
}

type quotaUpstreamWindow struct {
	Status   string `json:"status"`
	Percent  int    `json:"percent"`
	ResetsAt string `json:"resetsAt"`
}

func quotaIdentity(key string) (id, label string) {
	digest := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(digest[:])
	return managedAuthPrefix + hash, "OpenCode Go credential " + hash[:12]
}

// quotaProvider implements the host's native quota card surface (v8 SDK
// QuotaProvider). The deployed v2.3.0 plugin has no plugin-owned management
// page: quota renders on the host's credential cards exactly like the
// easycliproxyapi-commandcode-provider sibling.
type quotaProvider struct{ manager *Manager }

var _ pluginapi.QuotaProvider = quotaProvider{}

func (quotaProvider) Identifier() string { return ProviderID }

func (quotaProvider) DescribeQuota(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
	return pluginapi.QuotaDescribeResponse{
		SupportedProviders: []string{ProviderID},
		DisplayName:        "OpenCode Go",
		SupportsReset:      false,
	}, nil
}

func (p quotaProvider) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	if req.Provider != "" && req.Provider != ProviderID {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("quota provider handles %q credentials only", ProviderID)
	}
	key := quotaAuthKey(req.Attributes, req.Metadata)
	if key == "" {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("credential %q has no opencode-go api key", req.AuthID)
	}
	m := p.manager
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	plan, ok := quotaPlan(cfg, req.Attributes)
	if !ok {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("no enabled opencode-go plan is configured")
	}
	usage, err := fetchQuota(ctx, m.bridge, plan.BaseURL, cfg.RequestTimeout, key)
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, err
	}
	return quotaFetchResponseFromUsage(plan.Name, usage), nil
}

func (quotaProvider) ResetQuota(context.Context, pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
	return pluginapi.QuotaResetResponse{}, fmt.Errorf("opencode-go credentials do not support quota reset")
}

// quotaPlan resolves which plan a credential reports against. A credential
// carries no plan of its own (one key pool serves every plan), so an explicit
// `plan` attribute wins and otherwise the first enabled plan is used.
func quotaPlan(cfg config.Config, attributes map[string]string) (config.Plan, bool) {
	if name := strings.TrimSpace(attributes["plan"]); name != "" {
		if plan, ok := cfg.PlanByName(name); ok && plan.Enabled {
			return plan, true
		}
		return config.Plan{}, false
	}
	return cfg.DefaultPlan()
}

// quotaAuthKey resolves a credential's API key exactly the way the sibling
// easycliproxyapi-commandcode-provider plugin resolves it: the parsed
// attribute when present, otherwise the host's mutable metadata. host.auth.save
// exposes a freshly written credential through Metadata first and the
// auth-directory watcher re-parses it into Attributes later; both carry the
// SAME selected credential, so the quota card must read both or it blanks out
// in the window between the save and the re-parse.
func quotaAuthKey(attributes map[string]string, metadata map[string]any) string {
	if key := strings.TrimSpace(attributes["api_key"]); key != "" {
		return key
	}
	key, _ := metadata["api_key"].(string)
	return strings.TrimSpace(key)
}

// quotaFetchResponseFromUsage maps one account snapshot to the host's
// normalized quota shape: a subscription label, a plan summary metric and one
// bucket per rate-limit window. RemainingFraction is the fraction still
// available so a bar always reads as "left", never as "used". A window the
// upstream did not report is omitted rather than drawn as a full bar, which
// would claim capacity nobody measured.
func quotaFetchResponseFromUsage(planName string, usage quotaUsage) pluginapi.QuotaFetchResponse {
	out := pluginapi.QuotaFetchResponse{
		Subscription: &pluginapi.QuotaSubscription{Plan: planName, TierName: planName},
	}
	if quotaWindowPresent(usage.Monthly) {
		out.Summary = []pluginapi.QuotaMetric{{
			Key:    "monthly_remaining",
			Label:  "Monthly quota remaining",
			Value:  quotaRemainingPercent(usage.Monthly.Percent),
			Unit:   "percent",
			Format: "0",
		}}
	}
	buckets := make([]pluginapi.QuotaBucket, 0, 3)
	for _, w := range []struct {
		window      quotaWindow
		description string
	}{
		{usage.Rolling, "Rolling usage window"},
		{usage.Weekly, "Weekly usage window"},
		{usage.Monthly, "Monthly usage window"},
	} {
		if quotaWindowPresent(w.window) {
			buckets = append(buckets, quotaBucketFromWindow(w.window, w.description))
		}
	}
	if len(buckets) > 0 {
		out.Groups = []pluginapi.QuotaGroup{{
			DisplayName: "Usage windows",
			Buckets:     buckets,
		}}
	}
	return out
}

// quotaWindowPresent reports whether the upstream actually described a window.
func quotaWindowPresent(w quotaWindow) bool {
	return strings.TrimSpace(w.Status) != "" || w.Percent != 0
}

// quotaRemainingPercent is the fraction still available, in percent, clamped
// to 0..100: an upstream that reports a percent outside its own bounds must
// never produce a negative quota or a bar past its end.
func quotaRemainingPercent(percent int) float64 {
	switch {
	case percent < 0:
		return 100
	case percent > 100:
		return 0
	default:
		return float64(100 - percent)
	}
}

func quotaBucketFromWindow(w quotaWindow, description string) pluginapi.QuotaBucket {
	return pluginapi.QuotaBucket{
		Window:            w.Status,
		RemainingFraction: quotaRemainingPercent(w.Percent) / 100,
		ResetTime:         w.ResetsAt,
		Description:       description,
	}
}

func fetchQuota(ctx context.Context, bridge *HostBridge, baseURL string, timeout time.Duration, key string) (quotaUsage, error) {
	if bridge == nil {
		return quotaUsage{}, fmt.Errorf("quota bridge unavailable")
	}
	if timeout <= 0 || timeout > quotaMaxTimeout {
		timeout = quotaMaxTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := bridge.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: strings.TrimRight(baseURL, "/") + "/usage", Headers: http.Header{"Authorization": []string{"Bearer " + key}, "Accept": []string{"application/json"}}})
	if err != nil || resp.StatusCode != http.StatusOK {
		return quotaUsage{}, fmt.Errorf("quota upstream request failed")
	}
	var decoded usageResponse
	if err := json.Unmarshal(resp.Body, &decoded); err != nil {
		return quotaUsage{}, fmt.Errorf("quota response invalid")
	}
	return quotaUsage{
		Rolling: quotaWindow{Status: decoded.Usage.Rolling.Status, Percent: decoded.Usage.Rolling.Percent, ResetsAt: decoded.Usage.Rolling.ResetsAt},
		Weekly:  quotaWindow{Status: decoded.Usage.Weekly.Status, Percent: decoded.Usage.Weekly.Percent, ResetsAt: decoded.Usage.Weekly.ResetsAt},
		Monthly: quotaWindow{Status: decoded.Usage.Monthly.Status, Percent: decoded.Usage.Monthly.Percent, ResetsAt: decoded.Usage.Monthly.ResetsAt},
	}, nil
}
