package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This file pins the release identity of the repository, not the wire ABI:
// the plugin id, version and repository URL must stay in lockstep across the
// Go constants, registry.json, the release docs and the CI artifact config.
// A bump that forgets one of them fails here instead of shipping a store
// entry that advertises a version the binary does not report.

const (
	registryPath      = "../../registry.json"
	changelogPath     = "../../CHANGELOG.md"
	releaseNotesPath  = "../../RELEASE_NOTES.md"
	provenancePath    = "../../PROVENANCE.md"
	buildWorkflowPath = "../../.github/workflows/build.yml"
	testsWorkflowPath = "../../.github/workflows/tests.yml"
)

type registryFile struct {
	SchemaVersion int `json:"schema_version"`
	Plugins       []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Version     string `json:"version"`
		Repository  string `json:"repository"`
		Description string `json:"description"`
	} `json:"plugins"`
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func TestReleaseRegistryMatchesPluginIdentity(t *testing.T) {
	var registry registryFile
	if err := json.Unmarshal([]byte(readRepoFile(t, registryPath)), &registry); err != nil {
		t.Fatalf("registry.json is not valid JSON: %v", err)
	}
	if registry.SchemaVersion != 1 {
		t.Fatalf("registry schema_version = %d, want 1", registry.SchemaVersion)
	}
	if len(registry.Plugins) != 1 {
		t.Fatalf("registry entries = %d, want exactly one provider plugin", len(registry.Plugins))
	}
	entry := registry.Plugins[0]
	if entry.ID != pluginName {
		t.Fatalf("registry id = %q, want plugin id %q", entry.ID, pluginName)
	}
	if entry.Version != pluginVersion {
		t.Fatalf("registry version = %q, want plugin version %q", entry.Version, pluginVersion)
	}
	if entry.Repository != githubRepoURL {
		t.Fatalf("registry repository = %q, want %q", entry.Repository, githubRepoURL)
	}
	if strings.TrimSpace(entry.Name) == "" || strings.TrimSpace(entry.Description) == "" {
		t.Fatalf("registry entry lost its store display fields: %+v", entry)
	}
}

// TestReleaseDocsCarryCurrentVersion keeps the store-facing docs from lagging
// behind a version bump: the store falls back to registry.json and the
// release body is published verbatim from RELEASE_NOTES.md.
func TestReleaseDocsCarryCurrentVersion(t *testing.T) {
	if !regexp.MustCompile(`(?m)^## v` + regexp.QuoteMeta(pluginVersion) + `\b`).MatchString(readRepoFile(t, changelogPath)) {
		t.Fatalf("CHANGELOG.md has no `## v%s` heading", pluginVersion)
	}
	notes := readRepoFile(t, releaseNotesPath)
	if !strings.Contains(notes, pluginVersion) {
		t.Fatalf("RELEASE_NOTES.md does not mention version %s", pluginVersion)
	}
	if !strings.Contains(notes, pluginName) {
		t.Fatalf("RELEASE_NOTES.md does not mention plugin id %s", pluginName)
	}
	if !strings.Contains(readRepoFile(t, provenancePath), pluginVersion) {
		t.Fatalf("PROVENANCE.md does not mention version %s", pluginVersion)
	}
}

// TestReleaseArtifactNamesAreStable pins the do-not-rename contract: the
// store-polled repo URL, the plugin-store binding and the EasyCLI config key
// all depend on the runtime plugin id and the shared-library filename.
func TestReleaseArtifactNamesAreStable(t *testing.T) {
	if pluginName != "easycliproxyapi-opencode-provider" {
		t.Fatalf("runtime plugin id = %q, the EasyCLI config contract pins easycliproxyapi-opencode-provider", pluginName)
	}
	if pluginVersion != "2.5.2" {
		t.Fatalf("plugin version = %q, the released v2.5.2 surface is pinned", pluginVersion)
	}
	build := readRepoFile(t, buildWorkflowPath)
	for _, want := range []string{
		"PLUGIN_ID: " + pluginName,
		"LIB_BASE: easycliproxyapi-opencode-provider",
	} {
		if !strings.Contains(build, want) {
			t.Fatalf("build workflow is missing %q", want)
		}
	}
	tests := readRepoFile(t, testsWorkflowPath)
	if !strings.Contains(tests, "easycliproxyapi-opencode-provider.so") {
		t.Fatal("tests workflow no longer builds the deployed shared-library filename")
	}
}
