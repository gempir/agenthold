package harness_test

import (
	"encoding/json"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/harness"
	_ "github.com/gempir/agenthold/internal/harness/antigravity"
	_ "github.com/gempir/agenthold/internal/harness/claude"
	_ "github.com/gempir/agenthold/internal/harness/codex"
	_ "github.com/gempir/agenthold/internal/harness/fake"
	_ "github.com/gempir/agenthold/internal/harness/opencode"
)

// The layer builder checks the frozen Bun lockfile and runs the installed commands.
// This test ties the adapter inventory to the packages and commands that it checks.
func TestAdaptersMatchBaseImage(t *testing.T) {
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	readJSON(t, "../../images/harnesses/package.json", &pkg)
	versions := readEnv(t, "../../images/harnesses/versions.env")
	buildHarnesses := readText(t, "../../scripts/guest/build-harnesses.sh")
	smokeTest := regexp.MustCompile(`(?m)^for harness in (.+); do$`).FindStringSubmatch(buildHarnesses)
	if len(smokeTest) == 0 {
		t.Fatal("the harnesses builder does not smoke test the package commands")
	}
	packageCommands := strings.Fields(smokeTest[1])

	var ids, pkgs []string
	for _, ad := range harness.List(false) {
		ids = append(ids, ad.ID)
		switch {
		case ad.Package != "" && ad.Binary != "":
			t.Errorf("%s: both an npm package and a native binary", ad.ID)
		case ad.Binary != "":
			checkBinary(t, ad, versions, buildHarnesses)
		case ad.Package == "":
			t.Errorf("%s: no package or binary in the harnesses layer", ad.ID)
		default:
			pkgs = append(pkgs, ad.Package)
			want, ok := pkg.Dependencies[ad.Package]
			if !ok {
				t.Errorf("%s: %s is not in images/harnesses/package.json", ad.ID, ad.Package)
				continue
			}
			if !pinnedVersion.MatchString(want) {
				t.Errorf("%s: package.json must pin an exact version of %s, got %q", ad.ID, ad.Package, want)
			}
			if !slices.Contains(packageCommands, ad.Command[0]) {
				t.Errorf("%s runs %q, which the builder does not smoke test", ad.ID, ad.Command[0])
			}
		}
	}
	if got := strings.Join(ids, ","); got != "antigravity,claude-code,codex,opencode" {
		t.Errorf("harnesses %s: exactly Antigravity, Claude Code, Codex and OpenCode are supported", got)
	}
	slices.Sort(pkgs)
	if deps := slices.Sorted(maps.Keys(pkg.Dependencies)); !slices.Equal(deps, pkgs) {
		t.Errorf("the harnesses layer installs %v, the adapters use %v", deps, pkgs)
	}

	harnessEnv := readText(t, "../../images/harnesses/image.env")
	if !slices.Contains(strings.Split(harnessEnv, "\n"), "AGY_CLI_DISABLE_AUTO_UPDATE=true") {
		t.Error("images/harnesses/image.env does not set AGY_CLI_DISABLE_AUTO_UPDATE=true")
	}
}

var (
	pinnedVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	sha256Hex     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// checkBinary checks a native harness: an exact version and a sha256 per
// architecture in images/harnesses/versions.env, verified and linked into
// /usr/local/bin as Command[0] by scripts/guest/build-harnesses.sh.
func checkBinary(t *testing.T, ad *harness.Adapter, versions map[string]string, buildHarnesses string) {
	t.Helper()
	b := ad.Binary
	if v := versions[b+"_VERSION"]; !pinnedVersion.MatchString(v) {
		t.Errorf("%s: images/harnesses/versions.env pins %s_VERSION=%q, not an exact version", ad.ID, b, v)
	}
	for _, arch := range []string{"ARM64", "X64"} {
		key := b + "_SHA256_" + arch
		if !sha256Hex.MatchString(versions[key]) {
			t.Errorf("%s: images/harnesses/versions.env has no sha256 %s", ad.ID, key)
		}
		if !strings.Contains(buildHarnesses, "$"+key) {
			t.Errorf("%s: build-harnesses.sh does not check %s", ad.ID, key)
		}
	}
	if !strings.Contains(buildHarnesses, "${"+b+"_VERSION}") {
		t.Errorf("%s: build-harnesses.sh does not install %s_VERSION", ad.ID, b)
	}
	if !regexp.MustCompile(`(?m)^ln -sf \S+ "\$TREE/usr/local/bin/` + regexp.QuoteMeta(ad.Command[0]) + `"$`).MatchString(buildHarnesses) {
		t.Errorf("%s runs %q, which build-harnesses.sh does not link into /usr/local/bin", ad.ID, ad.Command[0])
	}
}

// readEnv reads KEY=VALUE lines (versions.env is sourced as shell but holds
// no expansions).
func readEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	env := map[string]string{}
	for line := range strings.Lines(readText(t, path)) {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			env[k] = v
		}
	}
	return env
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The base image must ship git and git-lfs and point git at the agenthold defaults, and
// image.env must stop git from prompting on the terminal.
func TestBaseImageIncludesGit(t *testing.T) {
	pkgs, err := os.ReadFile("../../images/base/packages.txt")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for line := range strings.SplitSeq(string(pkgs), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		have[line] = true
	}
	for _, p := range []string{"git", "git-lfs"} {
		if !have[p] {
			t.Errorf("images/base/packages.txt does not install %s", p)
		}
	}

	env, err := os.ReadFile("../../images/base/image.env")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(strings.Split(string(env), "\n"), "GIT_TERMINAL_PROMPT=0") {
		t.Error("images/base/image.env does not set GIT_TERMINAL_PROMPT=0")
	}

	prov, err := os.ReadFile("../../images/base/provision.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prov), "git config --system include.path /etc/agenthold/gitconfig") {
		t.Error("images/base/provision.sh does not include /etc/agenthold/gitconfig in the system git config")
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
