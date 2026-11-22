package acpstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/gempir/agenthold/internal/tunnel"
)

// ToolsSubdir is where per-sandbox ACP Registry agents are installed under the
// agent's persistent home directory (/home/agent/.agenthold/tools).
const ToolsSubdir = ".agenthold/tools"

// InstalledCommand is the resolved executable, args and env for a registry
// agent installed inside an agent's persistent home directory.
type InstalledCommand struct {
	Command []string          `json:"command"`
	Env     map[string]string `json:"env,omitempty"`
	Version string            `json:"version"`
	Dist    string            `json:"distribution"`
}

// receipt is written to ~/.agenthold/tools/<id>/<version>/receipt.json after
// installation so subsequent harness starts skip installation completely.
type receipt struct {
	AgentID      string            `json:"agentId"`
	Version      string            `json:"version"`
	Distribution string            `json:"distribution"`
	Command      []string          `json:"command"`
	Env          map[string]string `json:"env,omitempty"`
	BinDir       string            `json:"binDir,omitempty"`
}

// EnsureInstalled makes sure agent is installed in the sandbox's persistent
// ~/.agenthold/tools/<id>/<version>/ directory and returns the exact command
// and environment to launch it. If a valid receipt exists and the executable
// is still present, no download or package-manager command runs.
func EnsureInstalled(ctx context.Context, ts *tunnel.Session, home string, agent Agent, preferredDist string, status func(string)) (InstalledCommand, error) {
	if ts == nil {
		return InstalledCommand{}, errors.New("sandbox tunnel is not connected")
	}
	if home == "" {
		home = "/home/agent"
	}
	arch, err := detectGuestArch(ctx, ts)
	if err != nil {
		return InstalledCommand{}, fmt.Errorf("detect sandbox architecture: %w", err)
	}
	distKind, target, pkgDist, err := selectDistribution(agent, preferredDist, arch)
	if err != nil {
		return InstalledCommand{}, err
	}

	installDir := path.Join(home, ToolsSubdir, agent.ID, agent.Version)
	receiptPath := path.Join(installDir, "receipt.json")

	// Fast path: check existing receipt and executable.
	if rec, ok := readReceipt(ctx, ts, receiptPath); ok && rec.Version == agent.Version && rec.Distribution == distKind && len(rec.Command) > 0 {
		if checkExecutable(ctx, ts, rec.Command[0]) {
			return InstalledCommand{
				Command: rec.Command,
				Env:     rec.Env,
				Version: rec.Version,
				Dist:    rec.Distribution,
			}, nil
		}
	}

	if status != nil {
		status(fmt.Sprintf("Installing %s %s (%s) into sandbox…", agent.Name, agent.Version, distKind))
	}

	var rec receipt
	switch distKind {
	case "binary":
		rec, err = installBinary(ctx, ts, installDir, arch, agent, target)
	case "npx":
		rec, err = installNpx(ctx, ts, installDir, agent, pkgDist)
	case "uvx":
		rec, err = installUvx(ctx, ts, installDir, agent, pkgDist)
	default:
		return InstalledCommand{}, fmt.Errorf("unsupported distribution %q", distKind)
	}
	if err != nil {
		return InstalledCommand{}, err
	}

	raw, _ := json.MarshalIndent(rec, "", "  ")
	_ = ts.WriteFile(ctx, receiptPath, 0o644, bytes.NewReader(raw))

	return InstalledCommand{
		Command: rec.Command,
		Env:     rec.Env,
		Version: rec.Version,
		Dist:    rec.Distribution,
	}, nil
}

func detectGuestArch(ctx context.Context, ts *tunnel.Session) (string, error) {
	res, err := ts.Exec(ctx, []string{"uname", "-m"}, "", nil, 15, 4096)
	if err != nil {
		return "", err
	}
	m := strings.TrimSpace(res.Output)
	switch m {
	case "aarch64", "arm64":
		return "linux-aarch64", nil
	case "x86_64", "amd64":
		return "linux-x86_64", nil
	default:
		return "", fmt.Errorf("unsupported guest architecture %q", m)
	}
}

func selectDistribution(a Agent, pref, platformTarget string) (string, BinaryTarget, *PackageDistribution, error) {
	order := []string{"binary", "npx", "uvx"}
	if pref != "" && pref != "auto" {
		order = []string{pref}
	}
	for _, kind := range order {
		switch kind {
		case "binary":
			if bt, ok := a.Distribution.Binary[platformTarget]; ok {
				return "binary", bt, nil, nil
			}
		case "npx":
			if a.Distribution.Npx != nil {
				return "npx", BinaryTarget{}, a.Distribution.Npx, nil
			}
		case "uvx":
			if a.Distribution.Uvx != nil {
				return "uvx", BinaryTarget{}, a.Distribution.Uvx, nil
			}
		}
	}
	return "", BinaryTarget{}, nil, fmt.Errorf("ACP Registry agent %q (%s) has no compatible distribution for %s", a.ID, a.Version, platformTarget)
}

func readReceipt(ctx context.Context, ts *tunnel.Session, p string) (receipt, bool) {
	var buf bytes.Buffer
	if err := ts.ReadFile(ctx, p, nil, &buf); err != nil {
		return receipt{}, false
	}
	var r receipt
	if json.Unmarshal(buf.Bytes(), &r) != nil {
		return receipt{}, false
	}
	return r, true
}

func checkExecutable(ctx context.Context, ts *tunnel.Session, bin string) bool {
	res, err := ts.Exec(ctx, []string{"test", "-x", bin}, "", nil, 10, 256)
	return err == nil && res.ExitCode == 0
}

func archiveKind(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "raw"
	}
	p := strings.ToLower(u.Path)
	switch {
	case strings.HasSuffix(p, ".tar.gz"), strings.HasSuffix(p, ".tgz"):
		return "tar_gz"
	case strings.HasSuffix(p, ".tar.bz2"), strings.HasSuffix(p, ".tbz2"):
		return "tar_bz2"
	case strings.HasSuffix(p, ".zip"):
		return "zip"
	default:
		return "raw"
	}
}

func installBinary(ctx context.Context, ts *tunnel.Session, installDir, platformTarget string, a Agent, bt BinaryTarget) (receipt, error) {
	segs, ok := normalizeRelPath(bt.Cmd)
	if !ok {
		return receipt{}, fmt.Errorf("unsafe command path %q in registry entry %q", bt.Cmd, a.ID)
	}
	targetDir := path.Join(installDir, platformTarget)
	binPath := path.Join(append([]string{targetDir}, segs...)...)
	kind := archiveKind(bt.Archive)

	// Run atomic staged download, optional SHA-256 check, traversal-checked
	// extraction, and rename inside the sandbox.
	script := `set -euo pipefail
TARGET="$1"
URL="$2"
SHA="$3"
KIND="$4"
BIN="$5"
mkdir -p "$(dirname "$TARGET")"
STAGE="$(mktemp -d "${TARGET}.stage.XXXXXX")"
trap 'rm -rf "$STAGE"' EXIT
ARCHIVE="$STAGE/download"
curl -fsSL --retry 3 --max-time 600 -o "$ARCHIVE" "$URL"
if [ -n "$SHA" ]; then
  echo "$SHA  $ARCHIVE" | sha256sum -c --quiet -
fi
mkdir -p "$STAGE/out"
case "$KIND" in
  tar_gz)
    tar -tzf "$ARCHIVE" | grep -Ev '^(\./)?([^/]+(/|$))+$' && { echo "unsafe archive path" >&2; exit 1; } || true
    tar -tzf "$ARCHIVE" | grep -E '(^|/)\.\.(/|$)|^/' && { echo "archive path traversal rejected" >&2; exit 1; } || true
    tar -xzf "$ARCHIVE" -C "$STAGE/out" --no-same-owner
    ;;
  tar_bz2)
    tar -tjf "$ARCHIVE" | grep -E '(^|/)\.\.(/|$)|^/' && { echo "archive path traversal rejected" >&2; exit 1; } || true
    tar -xjf "$ARCHIVE" -C "$STAGE/out" --no-same-owner
    ;;
  zip)
    unzip -Z1 "$ARCHIVE" | grep -E '(^|/)\.\.(/|$)|^/' && { echo "archive path traversal rejected" >&2; exit 1; } || true
    unzip -q -o "$ARCHIVE" -d "$STAGE/out"
    ;;
  raw)
    mkdir -p "$(dirname "$STAGE/out/${BIN#"$TARGET/"}")"
    cp "$ARCHIVE" "$STAGE/out/${BIN#"$TARGET/"}"
    ;;
esac
rm -rf "$TARGET"
mv "$STAGE/out" "$TARGET"
chmod +x "$BIN"
test -x "$BIN"
`
	res, err := ts.Exec(ctx, []string{"bash", "-c", script, "_", targetDir, bt.Archive, strings.ToLower(bt.SHA256), kind, binPath}, "", nil, 600, 64*1024)
	if err != nil {
		return receipt{}, fmt.Errorf("install %s binary: %w", a.ID, err)
	}
	if res.ExitCode != 0 {
		return receipt{}, fmt.Errorf("install %s binary (exit %d): %s", a.ID, res.ExitCode, tailText(res.Output, 600))
	}
	cmd := append([]string{binPath}, bt.Args...)
	return receipt{
		AgentID:      a.ID,
		Version:      a.Version,
		Distribution: "binary",
		Command:      cmd,
		Env:          bt.Env,
		BinDir:       path.Dir(binPath),
	}, nil
}

func installNpx(ctx context.Context, ts *tunnel.Session, installDir string, a Agent, pkg *PackageDistribution) (receipt, error) {
	npmPrefix := path.Join(installDir, "npm")
	pkgName := pkg.Package
	if idx := strings.LastIndex(pkgName, "@"); idx > 0 {
		pkgName = pkgName[:idx]
	}
	script := `set -euo pipefail
PREFIX="$1"
SPEC="$2"
PKG="$3"
AGENT_ID="$4"
mkdir -p "$PREFIX"
if command -v bun >/dev/null 2>&1; then
  bun add --cwd "$PREFIX" --exact --trust "$SPEC" >&2
  js=bun
else
  # Older base images still ship Node.js and npm.
  npm install --prefix "$PREFIX" --omit=dev --no-fund --no-audit --no-update-notifier "$SPEC" >&2
  js=node
fi
"$js" -e '
const fs = require("fs");
const path = require("path");
const prefix = process.argv[1];
const pkg = process.argv[2];
const agentId = process.argv[3];
const manifest = JSON.parse(fs.readFileSync(path.join(prefix, "node_modules", pkg, "package.json"), "utf8"));
let binName;
if (typeof manifest.bin === "string") {
  binName = pkg.split("/").pop();
} else if (manifest.bin && typeof manifest.bin === "object") {
  const keys = Object.keys(manifest.bin);
  const unscoped = pkg.split("/").pop();
  const candidates = [agentId, unscoped, unscoped.replace(/-acp$/, "")];
  binName = candidates.find((c) => keys.includes(c)) || keys[0];
}
if (!binName) { process.exit(1); }
const binPath = path.join(prefix, "node_modules", ".bin", binName);
if (!fs.existsSync(binPath)) { process.exit(1); }
process.stdout.write(binPath);
' "$PREFIX" "$PKG" "$AGENT_ID"
`
	res, err := ts.Exec(ctx, []string{"bash", "-c", script, "_", npmPrefix, pkg.Package, pkgName, a.ID}, "", nil, 600, 64*1024)
	if err != nil {
		return receipt{}, fmt.Errorf("install %s from the npm registry: %w", a.ID, err)
	}
	if res.ExitCode != 0 {
		return receipt{}, fmt.Errorf("install %s from the npm registry (exit %d): %s", a.ID, res.ExitCode, tailText(res.Output, 600))
	}
	// Last line is the resolved executable path printed by the JavaScript runtime.
	lines := strings.Split(strings.TrimSpace(res.Output), "\n")
	binPath := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(binPath, npmPrefix) {
		return receipt{}, fmt.Errorf("install %s from the npm registry: could not resolve executable from %q", a.ID, tailText(res.Output, 400))
	}
	cmd := append([]string{binPath}, pkg.Args...)
	return receipt{
		AgentID:      a.ID,
		Version:      a.Version,
		Distribution: "npx",
		Command:      cmd,
		Env:          pkg.Env,
		BinDir:       path.Join(npmPrefix, "node_modules", ".bin"),
	}, nil
}

func installUvx(ctx context.Context, ts *tunnel.Session, installDir string, a Agent, pkg *PackageDistribution) (receipt, error) {
	pyRoot := path.Join(installDir, "python")
	pkgName := pkg.Package
	if idx := strings.LastIndex(pkgName, "=="); idx > 0 {
		pkgName = pkgName[:idx]
	} else if idx := strings.LastIndex(pkgName, "@"); idx > 0 {
		pkgName = pkgName[:idx]
	}
	pipSpec := strings.Replace(pkg.Package, "@", "==", 1)
	script := `set -euo pipefail
ROOT="$1"
SPEC="$2"
PKG="$3"
AGENT_ID="$4"
mkdir -p "$ROOT"
if command -v uv >/dev/null 2>&1; then
  UV_TOOL_DIR="$ROOT/tools" UV_TOOL_BIN_DIR="$ROOT/bin" uv tool install --force "$SPEC" >&2
  BIN_DIR="$ROOT/bin"
else
  python3 -m venv "$ROOT/venv" >&2
  "$ROOT/venv/bin/pip" install --quiet --no-input "$SPEC" >&2
  BIN_DIR="$ROOT/venv/bin"
fi
for c in "$AGENT_ID" "$PKG" "${PKG%-acp}"; do
  if [ -x "$BIN_DIR/$c" ]; then
    printf '%s' "$BIN_DIR/$c"
    exit 0
  fi
done
echo "could not locate installed executable in $BIN_DIR" >&2
exit 1
`
	res, err := ts.Exec(ctx, []string{"bash", "-c", script, "_", pyRoot, pipSpec, pkgName, a.ID}, "", nil, 600, 64*1024)
	if err != nil {
		return receipt{}, fmt.Errorf("install %s via python/uv: %w", a.ID, err)
	}
	if res.ExitCode != 0 {
		return receipt{}, fmt.Errorf("install %s via python/uv (exit %d): %s", a.ID, res.ExitCode, tailText(res.Output, 600))
	}
	lines := strings.Split(strings.TrimSpace(res.Output), "\n")
	binPath := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(binPath, pyRoot) {
		return receipt{}, fmt.Errorf("install %s via python/uv: could not resolve executable", a.ID)
	}
	cmd := append([]string{binPath}, pkg.Args...)
	return receipt{
		AgentID:      a.ID,
		Version:      a.Version,
		Distribution: "uvx",
		Command:      cmd,
		Env:          pkg.Env,
		BinDir:       path.Dir(binPath),
	}, nil
}

func tailText(s string, maxChars int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxChars {
		return s
	}
	return "…" + s[len(s)-maxChars:]
}
