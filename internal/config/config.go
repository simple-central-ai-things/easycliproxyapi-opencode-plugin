// Package config loads and validates the opencode-go plugin configuration
// per spec 04 (configuration) and spec 05 §2 (HTTPS/timeouts).
package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults (spec 04 §2/§4).
const (
	DefaultBaseURL          = "https://opencode.ai/zen/go/v1"
	DefaultModelPrefix      = "opencode-go"
	DefaultRefreshInterval  = 15 * time.Minute
	DefaultRequestTimeout   = 5 * time.Minute
	DefaultMaxResponseBytes = int64(67108864) // 64 MiB
)

type ModelPrefix struct {
	Enabled bool
	Value   string
}

// Plan is one EasyCLI "plan": a named upstream base URL plus the client-facing
// model prefix its catalog publishes. The deployed v2.3.0 config schema groups
// the upstream under `plans:` (e.g. `go` with prefix `opencode-go`, `zen` with
// prefix `opencode-zen`), so one provider keeps one key pool while each plan
// routes to its own upstream and model namespace.
type Plan struct {
	Name        string
	BaseURL     string
	CatalogURL  string
	ModelPrefix ModelPrefix
	Enabled     bool
}

type APIKey struct {
	Value string
}

type Catalog struct {
	RefreshInterval       time.Duration
	StaleWhileUnavailable bool
}

type Protocols struct {
	ChatCompletions bool
	Messages        bool
	Responses       bool
}

type RouteOverride struct {
	Protocol string `yaml:"protocol"`
	Endpoint string `yaml:"endpoint"`
}

type Config struct {
	// Plans are the enabled-by-config upstreams. BaseURL/ModelPrefix/
	// CatalogURL below mirror the primary plan (first enabled, else first
	// declared) so callers that predate plans keep one resolved view.
	Plans            []Plan
	BaseURL          string
	CatalogURL       string
	ModelPrefix      ModelPrefix
	APIKeys          []APIKey
	Catalog          Catalog
	Protocols        Protocols
	RouteOverrides   map[string]RouteOverride
	AllowHTTP        bool
	RequestTimeout   time.Duration
	MaxResponseBytes int64
}

// rawConfig mirrors the YAML shape; pointer fields distinguish "unset"
// (apply default) from explicitly-set values including "" (validate as-is).
// Unknown fields are ignored (host may pass extra keys).
type rawConfig struct {
	Plans            []rawPlan                `yaml:"plans"`
	BaseURL          *string                  `yaml:"base-url"`
	CatalogURL       *string                  `yaml:"catalog-url"`
	ModelPrefix      rawPrefix                `yaml:"model-prefix"`
	APIKeys          []rawKey                 `yaml:"api-keys"`
	Catalog          rawCatalog               `yaml:"catalog"`
	Protocols        rawProtocols             `yaml:"protocols"`
	RouteOverrides   map[string]RouteOverride `yaml:"route-overrides"`
	AllowHTTP        bool                     `yaml:"allow-http"`
	RequestTimeout   *string                  `yaml:"request-timeout"`
	MaxResponseBytes *int64                   `yaml:"max-response-bytes"`
}

// rawPlan mirrors one `plans:` entry. `enabled` defaults to true.
// `note` is operator documentation and is ignored.
type rawPlan struct {
	Name        string    `yaml:"name"`
	BaseURL     *string   `yaml:"base-url"`
	CatalogURL  *string   `yaml:"catalog-url"`
	ModelPrefix rawPrefix `yaml:"model-prefix"`
	Enabled     *bool     `yaml:"enabled"`
}

type rawPrefix struct {
	Enabled *bool   `yaml:"enabled"`
	Value   *string `yaml:"value"`
}

type rawKey struct {
	Value string `yaml:"value"`
}

type rawCatalog struct {
	RefreshInterval       *string `yaml:"refresh-interval"`
	StaleWhileUnavailable *bool   `yaml:"stale-while-unavailable"`
}

type rawProtocols struct {
	ChatCompletions *bool `yaml:"chat-completions"`
	Messages        *bool `yaml:"messages"`
	Responses       *bool `yaml:"responses"`
}

var (
	validProtocols = map[string]bool{"chat-completions": true, "messages": true, "responses": true}
)

// Load decodes YAML, expands ${VAR} references in api-key values only,
// applies defaults, and validates (spec 04 §6). Decode errors never echo
// decoded node values — a malformed entry (e.g. a bare-scalar API key)
// must not leak into the invalid_config envelope the host logs.
func Load(yamlBytes []byte) (Config, error) {
	var raw rawConfig
	if err := yaml.Unmarshal(yamlBytes, &raw); err != nil {
		if n := regexp.MustCompile(`line (\d+)`).FindStringSubmatch(err.Error()); n != nil {
			return Config{}, fmt.Errorf("decode config: invalid YAML structure near line %s", n[1])
		}
		return Config{}, fmt.Errorf("decode config: invalid YAML structure")
	}
	keys := make([]APIKey, len(raw.APIKeys))
	for i, k := range raw.APIKeys {
		keys[i] = APIKey{Value: os.ExpandEnv(k.Value)}
	}
	refreshInterval, err := parseDuration("catalog.refresh-interval", raw.Catalog.RefreshInterval, DefaultRefreshInterval)
	if err != nil {
		return Config{}, err
	}
	requestTimeout, err := parseDuration("request-timeout", raw.RequestTimeout, DefaultRequestTimeout)
	if err != nil {
		return Config{}, err
	}
	if requestTimeout <= 0 {
		return Config{}, fmt.Errorf("request-timeout: must be positive")
	}
	c := Config{
		APIKeys: keys,
		Catalog: Catalog{
			RefreshInterval:       refreshInterval,
			StaleWhileUnavailable: orDefault(raw.Catalog.StaleWhileUnavailable, true),
		},
		Protocols: Protocols{
			ChatCompletions: orDefault(raw.Protocols.ChatCompletions, true),
			Messages:        orDefault(raw.Protocols.Messages, true),
			Responses:       orDefault(raw.Protocols.Responses, true),
		},
		RouteOverrides:   raw.RouteOverrides,
		AllowHTTP:        raw.AllowHTTP,
		RequestTimeout:   requestTimeout,
		MaxResponseBytes: orDefault(raw.MaxResponseBytes, DefaultMaxResponseBytes),
	}
	// Legacy top-level base-url/model-prefix are the fallback for plans that
	// omit them; with no plans at all they define the single implicit plan,
	// so a v1.0.3 config keeps loading unchanged.
	topBaseURL := orDefault(raw.BaseURL, DefaultBaseURL)
	topPrefix := ModelPrefix{
		Enabled: orDefault(raw.ModelPrefix.Enabled, true),
		Value:   orDefault(raw.ModelPrefix.Value, DefaultModelPrefix),
	}
	var topCatalog *string
	if raw.CatalogURL != nil {
		trimmed := strings.TrimRight(*raw.CatalogURL, "/")
		topCatalog = &trimmed
	}
	if len(raw.Plans) == 0 {
		raw.Plans = []rawPlan{{
			Name:        "go",
			BaseURL:     &topBaseURL,
			ModelPrefix: rawPrefix{Enabled: &topPrefix.Enabled, Value: &topPrefix.Value},
			CatalogURL:  topCatalog,
		}}
	}
	plans := make([]Plan, len(raw.Plans))
	for i, rp := range raw.Plans {
		plan := Plan{
			Name:    strings.TrimSpace(rp.Name),
			BaseURL: strings.TrimRight(orDefault(rp.BaseURL, topBaseURL), "/"),
			ModelPrefix: ModelPrefix{
				Enabled: orDefault(rp.ModelPrefix.Enabled, topPrefix.Enabled),
				Value:   orDefault(rp.ModelPrefix.Value, topPrefix.Value),
			},
			Enabled: orDefault(rp.Enabled, true),
		}
		if rp.CatalogURL != nil {
			// Mirror the derived-default trim so an explicit trailing-slash
			// catalog-url cannot double up separators downstream.
			plan.CatalogURL = strings.TrimRight(*rp.CatalogURL, "/")
		} else {
			plan.CatalogURL = plan.BaseURL + "/models"
		}
		plans[i] = plan
	}
	c.Plans = plans
	if primary, ok := c.primaryPlan(); ok {
		c.BaseURL, c.CatalogURL, c.ModelPrefix = primary.BaseURL, primary.CatalogURL, primary.ModelPrefix
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// PublicID returns the client-facing model ID (FR-003).
func PublicID(c Config, upstreamID string) string {
	if plan, ok := c.primaryPlan(); ok {
		return PublicIDFor(plan, upstreamID)
	}
	return upstreamID
}

// PublicIDFor returns the client-facing model ID a plan publishes.
func PublicIDFor(plan Plan, upstreamID string) string {
	if plan.ModelPrefix.Enabled {
		return plan.ModelPrefix.Value + "/" + upstreamID
	}
	return upstreamID
}

// EffectivePlans returns the plans a directly constructed Config serves: the
// declared plans, or one implicit plan built from the legacy single
// base-url/model-prefix/catalog-url fields. Tests and embedders build Config
// structs by hand; they keep working without declaring plans.
func (c Config) EffectivePlans() []Plan {
	if len(c.Plans) > 0 {
		return c.Plans
	}
	return []Plan{{
		Name:        "go",
		BaseURL:     c.BaseURL,
		CatalogURL:  c.CatalogURL,
		ModelPrefix: c.ModelPrefix,
		Enabled:     true,
	}}
}

// primaryPlan is the first enabled plan, else the first declared plan.
func (c Config) primaryPlan() (Plan, bool) {
	plans := c.EffectivePlans()
	for _, p := range plans {
		if p.Enabled {
			return p, true
		}
	}
	if len(plans) > 0 {
		return plans[0], true
	}
	return Plan{}, false
}

// DefaultPlan returns the plan a credential without an explicit plan
// attribute reports quota against: the first enabled plan.
func (c Config) DefaultPlan() (Plan, bool) {
	for _, p := range c.EffectivePlans() {
		if p.Enabled {
			return p, true
		}
	}
	return Plan{}, false
}

// PlanByName resolves a configured plan by name, enabled or not.
func (c Config) PlanByName(name string) (Plan, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Plan{}, false
	}
	for _, p := range c.EffectivePlans() {
		if p.Name == name {
			return p, true
		}
	}
	return Plan{}, false
}

// EnabledPlans returns the plans that may serve traffic and catalogs, in
// declaration order.
func (c Config) EnabledPlans() []Plan {
	plans := c.EffectivePlans()
	out := make([]Plan, 0, len(plans))
	for _, p := range plans {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out
}

func (c Config) validate() error {
	plans := c.EffectivePlans()
	if len(plans) == 0 {
		return fmt.Errorf("plans: at least one plan is required")
	}
	enabled := 0
	prefixOwner := make(map[string]string, len(plans))
	unprefixed := 0
	names := make(map[string]bool, len(plans))
	for i, p := range plans {
		label := fmt.Sprintf("plans[%d]", i)
		if p.Name == "" {
			return fmt.Errorf("%s.name: must not be empty", label)
		}
		if !validPrefix(p.Name) {
			return fmt.Errorf("%s.name: invalid plan-name characters %q", label, p.Name)
		}
		if names[p.Name] {
			return fmt.Errorf("plans: duplicate plan name %q", p.Name)
		}
		names[p.Name] = true
		if err := validateURL(label+".base-url", p.BaseURL, c.AllowHTTP); err != nil {
			return err
		}
		if err := validateURL(label+".catalog-url", p.CatalogURL, c.AllowHTTP); err != nil {
			return err
		}
		if !p.Enabled {
			continue
		}
		enabled++
		if !p.ModelPrefix.Enabled {
			unprefixed++
			continue
		}
		if !validPrefix(p.ModelPrefix.Value) {
			return fmt.Errorf("%s.model-prefix.value: invalid provider-ID characters %q", label, p.ModelPrefix.Value)
		}
		if owner, ok := prefixOwner[p.ModelPrefix.Value]; ok {
			return fmt.Errorf("plans: plans %q and %q share model-prefix.value %q", owner, p.Name, p.ModelPrefix.Value)
		}
		prefixOwner[p.ModelPrefix.Value] = p.Name
	}
	if enabled == 0 {
		return fmt.Errorf("plans: at least one enabled plan is required")
	}
	if unprefixed > 1 {
		return fmt.Errorf("plans: at most one enabled plan may disable model-prefix (public model IDs would collide)")
	}
	if len(c.APIKeys) == 0 {
		return fmt.Errorf("api-keys: at least one key is required")
	}
	for i, k := range c.APIKeys {
		if k.Value == "" {
			return fmt.Errorf("api-keys[%d].value: expanded to empty", i)
		}
	}
	seen := make(map[string]bool, len(c.APIKeys))
	for _, k := range c.APIKeys {
		if seen[k.Value] {
			return fmt.Errorf("api-keys: duplicate key values are not allowed")
		}
		seen[k.Value] = true
	}
	if c.Catalog.RefreshInterval < time.Minute {
		return fmt.Errorf("catalog.refresh-interval: must be at least 1m")
	}
	if c.MaxResponseBytes <= 0 {
		return fmt.Errorf("max-response-bytes: must be positive")
	}
	for name, o := range c.RouteOverrides {
		if !validProtocols[o.Protocol] {
			return fmt.Errorf("route-overrides[%s].protocol: unsupported protocol %q", name, o.Protocol)
		}
		if o.Endpoint == "" {
			return fmt.Errorf("route-overrides[%s].endpoint: must not be empty", name)
		}
		if !strings.HasPrefix(o.Endpoint, "/") {
			return fmt.Errorf("route-overrides[%s].endpoint: must start with /", name)
		}
	}
	return nil
}

func validateURL(name, raw string, allowHTTP bool) error {
	if raw == "" {
		return fmt.Errorf("%s: must not be empty", name)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: invalid URL", name)
	}
	if u.Scheme == "" || u.Host == "" {
		// Echo only scheme://host — the configured string may embed
		// userinfo credentials (https://user:key@host).
		return fmt.Errorf("%s: invalid URL %s://%s", name, u.Scheme, u.Host)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("%s: must not contain query, fragment, or userinfo", name)
	}
	// Scheme allowlist (spec 05 §2): https always; http only behind
	// allow-http; anything else (ftp://, file://, custom schemes) is
	// rejected at load instead of failing at transport time.
	switch u.Scheme {
	case "https":
	case "http":
		if !allowHTTP {
			return fmt.Errorf("%s: http scheme requires allow-http", name)
		}
	default:
		return fmt.Errorf("%s: unsupported scheme %q; use https", name, u.Scheme)
	}
	return nil
}

func validPrefix(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if i == 0 && !alnum {
			return false
		}
		if !alnum && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func parseDuration(name string, p *string, def time.Duration) (time.Duration, error) {
	if p == nil {
		return def, nil
	}
	d, err := time.ParseDuration(*p)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration", name)
	}
	return d, nil
}

func orDefault[T any](p *T, def T) T {
	if p != nil {
		return *p
	}
	return def
}
