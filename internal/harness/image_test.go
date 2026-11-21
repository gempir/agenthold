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

// images/base/package.json is the only place npm harness versions are pinned
// (package-lock.json is generated from it), images/base/versions.env the only
// place native harness binaries are. Each adapter names its package or binary;
// this checks that the image installs exactly those, at one version, and that
// the command each adapter runs is a binary the image installs.
func TestAdaptersMatchBaseImage(t *testing.T) {
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	readJSON(t, "../../images/base/package.json", &pkg)
	var lock struct {
		Packages map[string]struct {
			Version      string            `json:"version"`
			Dependencies map[string]string `json:"dependencies"`
			Bin          map[string]string `json:"bin"`
		} `json:"packages"`
	}
	readJSON(t, "../../images/base/package-lock.json", &lock)
	if root := lock.Packages[""].Dependencies; !maps.Equal(root, pkg.Dependencies) {
		t.Fatalf("package-lock.json was not regenerated: it locks %v, package.json asks for %v", root, pkg.Dependencies)
	}
	versions := readEnv(t, "../../images/base/versions.env")
	provision := readText(t, "../../images/base/provision.sh")
	rootfs := readText(t, "../../scripts/guest/build-rootfs.sh")

	var ids, pkgs []string
	for _, ad := range harness.List(false) {
		ids = append(ids, ad.ID)
		switch {
		case ad.Package != "" && ad.Binary != "":
			t.Errorf("%s: both an npm package and a native binary", ad.ID)
		case ad.Binary != "":
			checkBinary(t, ad, versions, provision, rootfs)
		case ad.Package == "":
			t.Errorf("%s: no package or binary in the base image", ad.ID)
		default:
			pkgs = append(pkgs, ad.Package)
			want, ok := pkg.Dependencies[ad.Package]
			if !ok {
				t.Errorf("%s: %s is not in images/base/package.json", ad.ID, ad.Package)
				continue
			}
			locked := lock.Packages["node_modules/"+ad.Package]
			if locked.Version != want {
				t.Errorf("%s: package.json pins %s@%s, package-lock.json has %s", ad.ID, ad.Package, want, locked.Version)
			}
			if _, ok := locked.Bin[ad.Command[0]]; !ok {
				t.Errorf("%s runs %q, which %s does not install (bins %v)", ad.ID, ad.Command[0], ad.Package, slices.Collect(maps.Keys(locked.Bin)))
			}
		}
	}
	if got := strings.Join(ids, ","); got != "antigravity,claude-code,codex,opencode" {
		t.Errorf("harnesses %s: exactly Antigravity, Claude Code, Codex and OpenCode are supported", got)
	}
	slices.Sort(pkgs)
	if deps := slices.Sorted(maps.Keys(pkg.Dependencies)); !slices.Equal(deps, pkgs) {
		t.Errorf("the base image installs %v, the adapters use %v", deps, pkgs)
	}
}

var (
	pinnedVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	sha256Hex     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// checkBinary checks a native harness: an exact version and a sha256 per
// architecture in versions.env, verified by both the rootfs build (which
// downloads it) and provision.sh (which installs it as Command[0]).
func checkBinary(t *testing.T, ad *harness.Adapter, versions map[string]string, provision, rootfs string) {
	t.Helper()
	b := ad.Binary
	if v := versions[b+"_VERSION"]; !pinnedVersion.MatchString(v) {
		t.Errorf("%s: versions.env pins %s_VERSION=%q, not an exact version", ad.ID, b, v)
	}
	for _, arch := range []string{"ARM64", "X64"} {
		key := b + "_SHA256_" + arch
		if !sha256Hex.MatchString(versions[key]) {
			t.Errorf("%s: versions.env has no sha256 %s", ad.ID, key)
		}
		for name, script := range map[string]string{"provision.sh": provision, "build-rootfs.sh": rootfs} {
			if !strings.Contains(script, "$"+key) {
				t.Errorf("%s: %s does not check %s", ad.ID, name, key)
			}
		}
	}
	if !strings.Contains(provision, "${"+b+"_VERSION}") {
		t.Errorf("%s: provision.sh does not install %s_VERSION", ad.ID, b)
	}
	if !regexp.MustCompile(`(?m)^ln -sf \S+ /usr/local/bin/` + regexp.QuoteMeta(ad.Command[0]) + `$`).MatchString(provision) {
		t.Errorf("%s runs %q, which provision.sh does not link into /usr/local/bin", ad.ID, ad.Command[0])
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
