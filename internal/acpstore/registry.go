// Package acpstore implements the official ACP Registry ("ACP Store") client,
// search/ranking, validation, on-demand per-sandbox installer, and dynamic
// harness adapter resolution, ported from T3 Code's provider-acp-registry.
package acpstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// DefaultRegistryURL is the official ACP Registry JSON feed.
const DefaultRegistryURL = "https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"

const (
	maxRegistryBytes    = 1 << 20 // 1 MiB
	maxRegistryAgents   = 500
	defaultSearchLimit  = 30
	registryMaxAge      = 5 * time.Minute
	registryHTTPTimeout = 30 * time.Second
)

var (
	agentIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	versionPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	sha256Pattern      = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
	exactRunnerVersion = `v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?`
	exactNpxPattern    = regexp.MustCompile(`^(?:@[^/@\s]+/[^@\s]+|[a-z0-9][a-z0-9._-]*)@` + exactRunnerVersion + `$`)
	exactUvxPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(?:@|==)` + exactRunnerVersion + `$`)
)

// BuiltinRegistryIDs are registry entries already provided as first-class
// built-in harnesses in agenthold.
var BuiltinRegistryIDs = map[string]string{
	"claude-acp":      "claude-code",
	"claude-code":     "claude-code",
	"claude-code-acp": "claude-code",
	"codex-acp":       "codex",
	"opencode":        "opencode",
	"antigravity":     "antigravity",
	"antigravity-acp": "antigravity",
}

// BinaryTarget describes one platform-specific binary archive from the registry.
type BinaryTarget struct {
	Archive string            `json:"archive"`
	Cmd     string            `json:"cmd"`
	SHA256  string            `json:"sha256,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// PackageDistribution describes an npx or uvx distribution.
type PackageDistribution struct {
	Package string            `json:"package"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Distribution holds all distributions an ACP Registry agent publishes.
type Distribution struct {
	Binary map[string]BinaryTarget `json:"binary,omitempty"`
	Npx    *PackageDistribution    `json:"npx,omitempty"`
	Uvx    *PackageDistribution    `json:"uvx,omitempty"`
}

// Agent is one validated agent entry from the official ACP Registry.
type Agent struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	Description  string       `json:"description"`
	Authors      []string     `json:"authors,omitempty"`
	License      string       `json:"license,omitempty"`
	Website      string       `json:"website,omitempty"`
	Repository   string       `json:"repository,omitempty"`
	Icon         string       `json:"icon,omitempty"`
	Distribution Distribution `json:"distribution"`
}

// Index is a decoded ACP Registry snapshot.
type Index struct {
	Version string  `json:"version"`
	Agents  []Agent `json:"agents"`
}

// Catalog caches and searches the official ACP Registry.
type Catalog struct {
	URL      string
	CacheDir string
	Client   *http.Client

	mu        sync.Mutex
	index     *Index
	fetchedAt time.Time
}

// NewCatalog returns a Catalog backed by DefaultRegistryURL and an optional disk cache.
func NewCatalog(cacheDir string) *Catalog {
	return &Catalog{
		URL:      DefaultRegistryURL,
		CacheDir: cacheDir,
		Client:   &http.Client{Timeout: registryHTTPTimeout},
	}
}

// Fetch returns the registry index, using the in-memory cache when fresh unless
// forceRefresh is true. Falls back to the disk cache if the network fails.
func (c *Catalog) Fetch(ctx context.Context, forceRefresh bool) (*Index, error) {
	c.mu.Lock()
	if !forceRefresh && c.index != nil && time.Since(c.fetchedAt) < registryMaxAge {
		idx := c.index
		c.mu.Unlock()
		return idx, nil
	}
	c.mu.Unlock()

	raw, netErr := c.fetchHTTP(ctx)
	if netErr == nil {
		idx, err := DecodeIndex(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid ACP Registry response: %w", err)
		}
		c.writeDiskCache(raw)
		c.mu.Lock()
		c.index = idx
		c.fetchedAt = time.Now()
		c.mu.Unlock()
		return idx, nil
	}

	// Fall back to disk cache when offline or CDN unavailable.
	if cached := c.readDiskCache(); len(cached) > 0 {
		if idx, err := DecodeIndex(cached); err == nil {
			c.mu.Lock()
			c.index = idx
			c.mu.Unlock()
			return idx, nil
		}
	}
	c.mu.Lock()
	if c.index != nil {
		idx := c.index
		c.mu.Unlock()
		return idx, nil
	}
	c.mu.Unlock()
	return nil, fmt.Errorf("fetch ACP Registry: %w", netErr)
}

func (c *Catalog) fetchHTTP(ctx context.Context) ([]byte, error) {
	endpoint := c.URL
	if endpoint == "" {
		endpoint = DefaultRegistryURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: registryHTTPTimeout}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxRegistryBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxRegistryBytes {
		return nil, errors.New("registry payload exceeds 1 MiB")
	}
	return body, nil
}

func (c *Catalog) cachePath() string {
	if c.CacheDir == "" {
		return ""
	}
	return filepath.Join(c.CacheDir, "acp-registry", "registry.json")
}

func (c *Catalog) writeDiskCache(raw []byte) {
	p := c.cachePath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		_ = os.Rename(tmp, p)
	}
}

func (c *Catalog) readDiskCache() []byte {
	p := c.cachePath()
	if p == "" {
		return nil
	}
	b, _ := os.ReadFile(p)
	return b
}

// DecodeIndex validates and decodes an ACP Registry JSON document, dropping
// individual entries that violate URL/version/checksum constraints just like
// T3 Code's AcpRegistrySupport.ts.
func DecodeIndex(raw []byte) (*Index, error) {
	var env struct {
		Version string            `json:"version"`
		Agents  []json.RawMessage `json:"agents"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if !validVersion(env.Version) {
		return nil, fmt.Errorf("invalid registry version %q", env.Version)
	}
	if len(env.Agents) > maxRegistryAgents {
		env.Agents = env.Agents[:maxRegistryAgents]
	}
	out := &Index{Version: env.Version}
	for _, item := range env.Agents {
		var a Agent
		if json.Unmarshal(item, &a) != nil {
			continue
		}
		if validateAgent(&a) {
			out.Agents = append(out.Agents, a)
		}
	}
	return out, nil
}

func validHTTPS(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > 2048 {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

func validVersion(v string) bool {
	return len(v) > 0 && len(v) <= 128 && versionPattern.MatchString(v)
}

func validateAgent(a *Agent) bool {
	if len(a.ID) == 0 || len(a.ID) > 128 || !agentIDPattern.MatchString(a.ID) {
		return false
	}
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || len(a.Name) > 160 || !validVersion(a.Version) {
		return false
	}
	if len(a.Description) > 1024 {
		a.Description = a.Description[:1024]
	}
	if !validHTTPS(a.Website) || !validHTTPS(a.Repository) || !validHTTPS(a.Icon) {
		return false
	}
	if a.Distribution.Npx != nil {
		if !exactNpxPattern.MatchString(a.Distribution.Npx.Package) {
			a.Distribution.Npx = nil
		}
	}
	if a.Distribution.Uvx != nil {
		if !exactUvxPattern.MatchString(a.Distribution.Uvx.Package) {
			a.Distribution.Uvx = nil
		}
	}
	for plat, bt := range a.Distribution.Binary {
		if bt.Archive == "" || !validHTTPS(bt.Archive) || strings.TrimSpace(bt.Cmd) == "" {
			delete(a.Distribution.Binary, plat)
			continue
		}
		if bt.SHA256 != "" && !sha256Pattern.MatchString(bt.SHA256) {
			delete(a.Distribution.Binary, plat)
			continue
		}
		if _, ok := normalizeRelPath(bt.Cmd); !ok {
			delete(a.Distribution.Binary, plat)
		}
	}
	return len(a.Distribution.Binary) > 0 || a.Distribution.Npx != nil || a.Distribution.Uvx != nil
}

// SupportedDistributions returns the distribution kinds supported for a Linux
// guest of arch ("amd64" or "arm64", empty checks any linux target).
func SupportedDistributions(a Agent, arch string) []string {
	var out []string
	targets := linuxTargets(arch)
	for _, t := range targets {
		if _, ok := a.Distribution.Binary[t]; ok {
			out = append(out, "binary")
			break
		}
	}
	if a.Distribution.Npx != nil {
		out = append(out, "npx")
	}
	if a.Distribution.Uvx != nil {
		out = append(out, "uvx")
	}
	return out
}

func linuxTargets(arch string) []string {
	switch arch {
	case "arm64", "aarch64":
		return []string{"linux-aarch64"}
	case "amd64", "x86_64", "x64":
		return []string{"linux-x86_64"}
	default:
		return []string{"linux-x86_64", "linux-aarch64"}
	}
}

// SearchResult is one ranked entry returned by Catalog.Search.
type SearchResult struct {
	Agent         Agent    `json:"agent"`
	Distributions []string `json:"distributions"`
	BuiltinID     string   `json:"builtinId,omitempty"`
}

// Search ranks agents matching query, matching T3 Code's searchRank().
func (c *Catalog) Search(ctx context.Context, query string, limit int, arch string) ([]SearchResult, string, error) {
	idx, err := c.Fetch(ctx, false)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > 100 {
		limit = 100
	}
	type scored struct {
		rank int
		res  SearchResult
	}
	var hits []scored
	for _, a := range idx.Agents {
		dists := SupportedDistributions(a, arch)
		if len(dists) == 0 {
			continue
		}
		rank, ok := searchRank(a, query)
		if !ok {
			continue
		}
		hits = append(hits, scored{
			rank: rank,
			res: SearchResult{
				Agent:         a,
				Distributions: dists,
				BuiltinID:     BuiltinRegistryIDs[a.ID],
			},
		})
	}
	slices.SortFunc(hits, func(a, b scored) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		return strings.Compare(strings.ToLower(a.res.Agent.Name), strings.ToLower(b.res.Agent.Name))
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]SearchResult, len(hits))
	for i, h := range hits {
		out[i] = h.res
	}
	return out, idx.Version, nil
}

// Lookup fetches the latest registry index and returns agentID.
func (c *Catalog) Lookup(ctx context.Context, agentID string) (Agent, error) {
	idx, err := c.Fetch(ctx, false)
	if err != nil {
		return Agent{}, err
	}
	for _, a := range idx.Agents {
		if a.ID == agentID {
			return a, nil
		}
	}
	// Refresh once on miss in case the registry just published a new agent.
	idx, err = c.Fetch(ctx, true)
	if err != nil {
		return Agent{}, err
	}
	for _, a := range idx.Agents {
		if a.ID == agentID {
			return a, nil
		}
	}
	return Agent{}, fmt.Errorf("ACP Registry does not contain agent %q", agentID)
}

func searchRank(a Agent, query string) (int, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 100, true
	}
	id := strings.ToLower(a.ID)
	name := strings.ToLower(a.Name)
	authors := strings.ToLower(strings.Join(a.Authors, " "))
	desc := strings.ToLower(a.Description)
	if id == q || name == q {
		return 0, true
	}
	if strings.HasPrefix(id, q) || strings.HasPrefix(name, q) {
		return 10, true
	}
	terms := strings.Fields(q)
	tokens := strings.FieldsFunc(id+" "+name, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	allMatch := func(pred func(string) bool) bool {
		for _, t := range terms {
			if !pred(t) {
				return false
			}
		}
		return true
	}
	if allMatch(func(t string) bool {
		for _, tok := range tokens {
			if strings.HasPrefix(tok, t) {
				return true
			}
		}
		return false
	}) {
		return 20, true
	}
	if allMatch(func(t string) bool { return strings.Contains(id, t) || strings.Contains(name, t) }) {
		return 30, true
	}
	if allMatch(func(t string) bool { return strings.Contains(authors, t) }) {
		return 40, true
	}
	haystack := id + " " + name + " " + authors + " " + desc
	if allMatch(func(t string) bool { return strings.Contains(haystack, t) }) {
		return 50, true
	}
	return 0, false
}

// normalizeRelPath rejects absolute paths and traversal segments ("." / "..").
func normalizeRelPath(p string) ([]string, bool) {
	s := strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	s = strings.TrimPrefix(s, "./")
	if s == "" || strings.HasPrefix(s, "/") || (len(s) >= 2 && s[1] == ':') {
		return nil, false
	}
	raw := strings.Split(s, "/")
	var segs []string
	for _, seg := range raw {
		if seg == "" {
			continue
		}
		if seg == "." || seg == ".." {
			return nil, false
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 {
		return nil, false
	}
	return segs, true
}
