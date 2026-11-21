// Package images manages enterprise custom image recipes, multi-arch builds,
// content-addressed layer storage, and cross-node layer distribution.
package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

const (
	// BuiltinBaseID is the reserved identifier representing the built-in base image.
	BuiltinBaseID = "base"
	// BuiltinBaseName is the display name of the built-in default base image.
	BuiltinBaseName = "agenthold Base"
	// MaxBuildLogBytes bounds the persisted and live-buffered build log per build.
	MaxBuildLogBytes = 2 << 20
	// MaxSetupScriptBytes bounds the bash setup script stored in an image recipe.
	MaxSetupScriptBytes = 256 << 10
	// MaxAptPackages bounds the APT package list in an image recipe.
	MaxAptPackages = 200
)

// Sentinel errors returned by Manager.
var (
	ErrNotFound      = errors.New("image not found")
	ErrBuildNotFound = errors.New("image build not found")
	ErrNameTaken     = errors.New("an image with that name already exists")
	ErrImageInUse    = errors.New("image is still used by one or more agents or apps")
	ErrNotBuilt      = errors.New("image has no ready build yet; build it first")
	ErrNoBuildNode   = errors.New("no connected node is available to build images")
	ErrValidation    = errors.New("invalid image configuration")
)

var (
	aptPkgRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.:-]{0,127}$`)
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

// LayerRef returns the logical layer reference passed in sandbox.Spec.Layers
// for a custom image revision.
func LayerRef(imageID string, revision int64) string {
	return fmt.Sprintf("image:%s:%d", imageID, revision)
}

// NodeProvider supplies live connected nodes for building and pushing layers.
type NodeProvider interface {
	LayerHost(ctx context.Context, nodeID string) (node.LayerHost, string, error)
	ConnectedLayerHosts(ctx context.Context) []node.ConnectedNode
}

// SingleNodeProvider adapts a bare *node.Local (for tests without a Cluster).
type SingleNodeProvider struct {
	Local *node.Local
}

// LayerHost implements NodeProvider.
func (s SingleNodeProvider) LayerHost(_ context.Context, _ string) (node.LayerHost, string, error) {
	if s.Local == nil {
		return nil, "", ErrNoBuildNode
	}
	return s.Local, runtime.GOARCH, nil
}

// ConnectedLayerHosts implements NodeProvider.
func (s SingleNodeProvider) ConnectedLayerHosts(_ context.Context) []node.ConnectedNode {
	if s.Local == nil {
		return nil
	}
	return []node.ConnectedNode{{ID: node.LocalID, Arch: runtime.GOARCH, Host: s.Local}}
}

// Recipe is the declarative specification of a custom image.
type Recipe struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	AptPackages []string          `json:"aptPackages"`
	SetupScript string            `json:"setupScript"`
	Env         map[string]string `json:"env"`
	SecretIDs   []string          `json:"secretIds"`
	IsDefault   *bool             `json:"isDefault,omitempty"`
}

// ImageSummary pairs a DB image row with parsed recipe fields and build status.
type ImageSummary struct {
	Row         db.Image
	AptPackages []string
	Env         map[string]string
	SecretIDs   []string
	ReadyArches []string
	Building    bool
	AgentCount  int64
	LatestBuild *db.ImageBuild
}

// Config configures a Manager.
type Config struct {
	Store             *store.Store
	DataDir           string
	Nodes             NodeProvider
	Box               *secrets.Box
	ExplicitProxyPort int
	Log               *slog.Logger
	Now               func() time.Time
}

// Manager coordinates custom image recipes, Firecracker builder runs, and
// cross-node layer caching.
type Manager struct {
	Config

	mu       sync.Mutex
	liveLogs map[string]*logBuffer
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

type logBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

func (b *logBuffer) WriteLine(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() >= MaxBuildLogBytes {
		b.truncated = true
		return
	}
	b.buf.WriteString(line)
	b.buf.WriteByte('\n')
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf.String()
	if b.truncated {
		s += "\n... (build log truncated at 2 MiB)\n"
	}
	return s
}

// NewManager initializes a custom images manager. Call Start before serving
// requests and Close during shutdown.
func NewManager(cfg Config) *Manager {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		Config:   cfg,
		liveLogs: map[string]*logBuffer{},
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Start reconciles any builds interrupted by a prior restart.
func (m *Manager) Start(ctx context.Context) error {
	return m.Store.MarkStaleImageBuildsFailed(ctx, m.Now().Unix())
}

// Close cancels in-flight build goroutines and waits for them to unwind.
func (m *Manager) Close() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}

func (m *Manager) layerStore() *node.LayerStore {
	return node.NewLayerStore(m.DataDir)
}

// ValidateRecipe checks and normalizes a Recipe.
func ValidateRecipe(r *Recipe) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Name) > 80 {
		return fmt.Errorf("%w: name must be 1 to 80 characters", ErrValidation)
	}
	if strings.EqualFold(r.Name, BuiltinBaseName) || strings.EqualFold(r.Name, BuiltinBaseID) {
		return fmt.Errorf("%w: name %q is reserved for the built-in base image", ErrValidation, r.Name)
	}
	r.Description = strings.TrimSpace(r.Description)
	if len(r.Description) > 500 {
		return fmt.Errorf("%w: description must be at most 500 characters", ErrValidation)
	}
	if len(r.AptPackages) > MaxAptPackages {
		return fmt.Errorf("%w: at most %d apt packages", ErrValidation, MaxAptPackages)
	}
	seenPkg := map[string]bool{}
	pkgs := make([]string, 0, len(r.AptPackages))
	for _, p := range r.AptPackages {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !aptPkgRe.MatchString(p) {
			return fmt.Errorf("%w: invalid apt package name %q", ErrValidation, p)
		}
		if !seenPkg[p] {
			seenPkg[p] = true
			pkgs = append(pkgs, p)
		}
	}
	r.AptPackages = pkgs
	if len(r.SetupScript) > MaxSetupScriptBytes {
		return fmt.Errorf("%w: setupScript exceeds %d bytes", ErrValidation, MaxSetupScriptBytes)
	}
	if strings.ContainsRune(r.SetupScript, '\x00') {
		return fmt.Errorf("%w: setupScript contains NUL byte", ErrValidation)
	}
	cleanEnv := make(map[string]string, len(r.Env))
	for k, v := range r.Env {
		k = strings.TrimSpace(k)
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("%w: invalid environment variable name %q", ErrValidation, k)
		}
		if strings.ContainsAny(v, "\x00\r\n") {
			return fmt.Errorf("%w: environment variable %q must be single-line", ErrValidation, k)
		}
		cleanEnv[k] = v
	}
	r.Env = cleanEnv
	seenSec := map[string]bool{}
	secs := make([]string, 0, len(r.SecretIDs))
	for _, s := range r.SecretIDs {
		s = strings.TrimSpace(s)
		if s == "" || seenSec[s] {
			continue
		}
		seenSec[s] = true
		secs = append(secs, s)
	}
	r.SecretIDs = secs
	return nil
}

// Create creates a new custom image definition.
func (m *Manager) Create(ctx context.Context, userID string, r Recipe) (ImageSummary, error) {
	if err := ValidateRecipe(&r); err != nil {
		return ImageSummary{}, err
	}
	if err := m.verifySecrets(ctx, userID, r.SecretIDs); err != nil {
		return ImageSummary{}, err
	}
	aptJSON, _ := json.Marshal(r.AptPackages)
	envJSON, _ := json.Marshal(r.Env)
	secJSON, _ := json.Marshal(r.SecretIDs)
	now := m.Now().Unix()
	imgID := id.New("img")
	isDef := int64(0)
	if r.IsDefault != nil && *r.IsDefault {
		isDef = 1
	}
	err := m.Store.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.GetImageByName(ctx, r.Name); err == nil {
			return ErrNameTaken
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if isDef == 1 {
			if err := q.ClearDefaultImages(ctx); err != nil {
				return err
			}
		}
		return q.CreateImage(ctx, db.CreateImageParams{
			ID: imgID, Name: r.Name, Description: r.Description,
			AptPackages: string(aptJSON), SetupScript: r.SetupScript,
			EnvJson: string(envJSON), SecretIds: string(secJSON),
			IsDefault: isDef, CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
		})
	})
	if err != nil {
		return ImageSummary{}, err
	}
	return m.Get(ctx, imgID)
}

// Update modifies a custom image's recipe and/or default flag.
func (m *Manager) Update(ctx context.Context, userID, imageID string, r Recipe) (ImageSummary, error) {
	if err := ValidateRecipe(&r); err != nil {
		return ImageSummary{}, err
	}
	if err := m.verifySecrets(ctx, userID, r.SecretIDs); err != nil {
		return ImageSummary{}, err
	}
	aptJSON, _ := json.Marshal(r.AptPackages)
	envJSON, _ := json.Marshal(r.Env)
	secJSON, _ := json.Marshal(r.SecretIDs)
	now := m.Now().Unix()
	err := m.Store.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.GetImage(ctx, imageID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if existing, err := q.GetImageByName(ctx, r.Name); err == nil && existing.ID != imageID {
			return ErrNameTaken
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := q.UpdateImage(ctx, db.UpdateImageParams{
			ID: imageID, Name: r.Name, Description: r.Description,
			AptPackages: string(aptJSON), SetupScript: r.SetupScript,
			EnvJson: string(envJSON), SecretIds: string(secJSON),
			UpdatedAt: now,
		}); err != nil {
			return err
		}
		if r.IsDefault != nil {
			if *r.IsDefault {
				if err := q.ClearDefaultImages(ctx); err != nil {
					return err
				}
				return q.SetImageDefault(ctx, db.SetImageDefaultParams{IsDefault: 1, UpdatedAt: now, ID: imageID})
			}
			return q.SetImageDefault(ctx, db.SetImageDefaultParams{IsDefault: 0, UpdatedAt: now, ID: imageID})
		}
		return nil
	})
	if err != nil {
		return ImageSummary{}, err
	}
	return m.Get(ctx, imageID)
}

// Delete removes a custom image when no agent or app references it, and cleans
// up any orphaned compressed layer archives on the controller.
func (m *Manager) Delete(ctx context.Context, imageID string) error {
	if _, err := m.Store.GetImage(ctx, imageID); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	n, err := m.Store.CountImageAgents(ctx, imageID)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w (%d machines)", ErrImageInUse, n)
	}
	builds, err := m.Store.ListImageBuilds(ctx, imageID)
	if err != nil {
		return err
	}
	if err := m.Store.DeleteImage(ctx, imageID); err != nil {
		return err
	}
	ls := m.layerStore()
	seen := map[string]bool{}
	for _, b := range builds {
		if b.LayerSha256 == "" || seen[b.LayerSha256] {
			continue
		}
		seen[b.LayerSha256] = true
		if cnt, err := m.Store.CountLayerSHAInBuilds(ctx, b.LayerSha256); err == nil && cnt == 0 {
			_ = ls.DeleteZstd(b.LayerSha256)
		}
	}
	return nil
}

// Get returns one custom image with its latest build summary.
func (m *Manager) Get(ctx context.Context, imageID string) (ImageSummary, error) {
	row, err := m.Store.GetImage(ctx, imageID)
	if errors.Is(err, sql.ErrNoRows) {
		return ImageSummary{}, ErrNotFound
	}
	if err != nil {
		return ImageSummary{}, err
	}
	builds, err := m.Store.ListImageBuilds(ctx, imageID)
	if err != nil {
		return ImageSummary{}, err
	}
	cnt, err := m.Store.CountImageAgents(ctx, imageID)
	if err != nil {
		return ImageSummary{}, err
	}
	return summarizeImage(row, builds, cnt), nil
}

// List returns all custom images with their build summaries.
func (m *Manager) List(ctx context.Context) ([]ImageSummary, error) {
	rows, err := m.Store.ListImages(ctx)
	if err != nil {
		return nil, err
	}
	allBuilds, err := m.Store.ListAllImageBuilds(ctx)
	if err != nil {
		return nil, err
	}
	byImage := map[string][]db.ImageBuild{}
	for _, b := range allBuilds {
		byImage[b.ImageID] = append(byImage[b.ImageID], b)
	}
	out := make([]ImageSummary, 0, len(rows))
	for _, r := range rows {
		cnt, err := m.Store.CountImageAgents(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, summarizeImage(r, byImage[r.ID], cnt))
	}
	return out, nil
}

func summarizeImage(row db.Image, builds []db.ImageBuild, agentCount int64) ImageSummary {
	var apt []string
	_ = json.Unmarshal([]byte(row.AptPackages), &apt)
	if apt == nil {
		apt = []string{}
	}
	env := map[string]string{}
	_ = json.Unmarshal([]byte(row.EnvJson), &env)
	var secs []string
	_ = json.Unmarshal([]byte(row.SecretIds), &secs)
	if secs == nil {
		secs = []string{}
	}
	var readyArches []string
	building := false
	var latest *db.ImageBuild
	for i := range builds {
		b := &builds[i]
		if latest == nil {
			latest = b
		}
		if b.Status == "queued" || b.Status == "building" {
			building = true
		}
		if row.LatestRev > 0 && b.Revision == row.LatestRev && b.Status == "ready" {
			if !slices.Contains(readyArches, b.Arch) {
				readyArches = append(readyArches, b.Arch)
			}
		}
	}
	slices.Sort(readyArches)
	return ImageSummary{
		Row:         row,
		AptPackages: apt,
		Env:         env,
		SecretIDs:   secs,
		ReadyArches: readyArches,
		Building:    building,
		AgentCount:  agentCount,
		LatestBuild: latest,
	}
}

// StartBuild schedules a new revision build of imageID across every architecture
// present among currently connected nodes (one build per architecture).
func (m *Manager) StartBuild(ctx context.Context, userID, imageID string) ([]db.ImageBuild, error) {
	img, err := m.Get(ctx, imageID)
	if err != nil {
		return nil, err
	}
	if m.Nodes == nil {
		return nil, ErrNoBuildNode
	}
	hosts := m.Nodes.ConnectedLayerHosts(ctx)
	if len(hosts) == 0 {
		return nil, ErrNoBuildNode
	}
	// Pick the best (highest available memory) connected node per architecture.
	byArch := map[string]node.ConnectedNode{}
	var arches []string
	for _, h := range hosts {
		if _, exists := byArch[h.Arch]; !exists {
			byArch[h.Arch] = h
			arches = append(arches, h.Arch)
		}
	}
	slices.Sort(arches)

	egressCfg, buildEnv, err := m.resolveBuildSecrets(ctx, userID, img.SecretIDs)
	if err != nil {
		return nil, err
	}
	if m.ExplicitProxyPort > 0 {
		merged := egress.SandboxEnv(strconv.Itoa(m.ExplicitProxyPort))
		for k, v := range buildEnv {
			merged[k] = v
		}
		buildEnv = merged
	}

	recipeSnap, _ := json.Marshal(map[string]any{
		"name":        img.Row.Name,
		"aptPackages": img.AptPackages,
		"setupScript": img.Row.SetupScript,
		"env":         img.Env,
		"secretCount": len(img.SecretIDs),
	})

	now := m.Now().Unix()
	var created []db.ImageBuild
	err = m.Store.Tx(ctx, func(q *db.Queries) error {
		maxRev, err := q.MaxImageRevision(ctx, imageID)
		if err != nil {
			return err
		}
		rev := maxRev + 1
		for _, arch := range arches {
			target := byArch[arch]
			b := db.ImageBuild{
				ID:         id.New("bld"),
				ImageID:    imageID,
				Revision:   rev,
				Arch:       arch,
				NodeID:     target.ID,
				Status:     "queued",
				RecipeJson: string(recipeSnap),
				CreatedAt:  now,
			}
			if err := q.CreateImageBuild(ctx, db.CreateImageBuildParams{
				ID: b.ID, ImageID: b.ImageID, Revision: b.Revision, Arch: b.Arch,
				NodeID: b.NodeID, Status: b.Status, RecipeJson: b.RecipeJson, CreatedAt: b.CreatedAt,
			}); err != nil {
				return err
			}
			created = append(created, b)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, b := range created {
		target := byArch[b.Arch]
		m.wg.Add(1)
		go func(build db.ImageBuild, cn node.ConnectedNode) {
			defer m.wg.Done()
			m.runBuild(build, img, cn, egressCfg, buildEnv)
		}(b, target)
	}
	return created, nil
}

func (m *Manager) runBuild(build db.ImageBuild, img ImageSummary, target node.ConnectedNode, egressCfg *node.AgentConfig, buildEnv map[string]string) {
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Minute)
	defer cancel()

	lb := &logBuffer{}
	m.mu.Lock()
	m.liveLogs[build.ID] = lb
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.liveLogs, build.ID)
		m.mu.Unlock()
	}()

	_ = m.Store.StartImageBuild(ctx, db.StartImageBuildParams{
		NodeID: target.ID, StartedAt: m.Now().Unix(), ID: build.ID,
	})
	lb.WriteLine(fmt.Sprintf("==> Building %s rev %d (%s) on node %s", img.Row.Name, build.Revision, build.Arch, target.ID))

	req := node.LayerBuildRequest{
		BuildID:      build.ID,
		ImageID:      build.ImageID,
		Revision:     build.Revision,
		AptPackages:  img.AptPackages,
		SetupScript:  img.Row.SetupScript,
		Env:          img.Env,
		EgressConfig: egressCfg,
		BuildEnv:     buildEnv,
		CPUs:         2,
		MemoryMB:     2048,
		DiskGB:       8,
	}
	res, artifactStream, err := target.Host.BuildLayer(ctx, req, lb.WriteLine)
	if err != nil {
		m.failBuild(build.ID, lb, err)
		return
	}
	defer artifactStream.Close()

	res.Artifact.Ref = LayerRef(build.ImageID, build.Revision)
	if err := m.layerStore().SaveZstd(res.Artifact, artifactStream); err != nil {
		m.failBuild(build.ID, lb, fmt.Errorf("store built layer: %w", err))
		return
	}
	fin := m.Now().Unix()
	if err := m.Store.CompleteImageBuild(context.Background(), db.CompleteImageBuildParams{
		BaseKey:     res.BaseKey,
		LayerSha256: res.Artifact.SHA256,
		LayerSize:   res.Artifact.Size,
		ZstdSha256:  res.Artifact.ZstdSHA256,
		ZstdSize:    res.Artifact.ZstdSize,
		LogText:     lb.String(),
		FinishedAt:  fin,
		ID:          build.ID,
	}); err != nil {
		m.Log.Error("complete image build", "build", build.ID, "err", err)
		return
	}
	if err := m.Store.SetImageLatestRev(context.Background(), db.SetImageLatestRevParams{
		LatestRev: build.Revision, UpdatedAt: fin, ID: build.ImageID,
	}); err != nil {
		m.Log.Error("update image latest rev", "image", build.ImageID, "err", err)
	}
}

func (m *Manager) failBuild(buildID string, lb *logBuffer, err error) {
	lb.WriteLine("ERROR: " + err.Error())
	m.Log.Warn("image build failed", "build", buildID, "err", err)
	_ = m.Store.FailImageBuild(context.Background(), db.FailImageBuildParams{
		Error:      err.Error(),
		LogText:    lb.String(),
		FinishedAt: m.Now().Unix(),
		ID:         buildID,
	})
}

// GetBuild returns a build record, substituting its live log buffer while running.
func (m *Manager) GetBuild(ctx context.Context, imageID, buildID string) (db.ImageBuild, error) {
	b, err := m.Store.GetImageBuild(ctx, buildID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && b.ImageID != imageID) {
		return db.ImageBuild{}, ErrBuildNotFound
	}
	if err != nil {
		return db.ImageBuild{}, err
	}
	m.mu.Lock()
	lb := m.liveLogs[buildID]
	m.mu.Unlock()
	if lb != nil {
		b.LogText = lb.String()
	}
	return b, nil
}

// ListBuilds returns all builds of imageID, newest revision first.
func (m *Manager) ListBuilds(ctx context.Context, imageID string) ([]db.ImageBuild, error) {
	if _, err := m.Store.GetImage(ctx, imageID); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	return m.Store.ListImageBuilds(ctx, imageID)
}

// ResolveForNewMachine resolves the requested imageId for a new agent or app:
//   - "base": built-in agenthold Base ("", 0, nil)
//   - "": org default custom image if one is set and built, else built-in base
//   - "<img_id>": that custom image at its latest ready revision
func (m *Manager) ResolveForNewMachine(ctx context.Context, requestedID string) (imageID string, rev int64, readyArches []string, err error) {
	requestedID = strings.TrimSpace(requestedID)
	if requestedID == BuiltinBaseID {
		return "", 0, nil, nil
	}
	if requestedID == "" {
		def, err := m.Store.GetDefaultImage(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, nil, nil
		}
		if err != nil {
			return "", 0, nil, err
		}
		if def.LatestRev == 0 {
			return "", 0, nil, nil
		}
		requestedID = def.ID
	}
	img, err := m.Get(ctx, requestedID)
	if err != nil {
		return "", 0, nil, err
	}
	if img.Row.LatestRev == 0 || len(img.ReadyArches) == 0 {
		return "", 0, nil, ErrNotBuilt
	}
	return img.Row.ID, img.Row.LatestRev, img.ReadyArches, nil
}

// ReadyArchesFor returns the architectures that have a ready build for
// (imageID, rev). Returns nil when imageID is empty (built-in base).
func (m *Manager) ReadyArchesFor(ctx context.Context, imageID string, rev int64) ([]string, error) {
	if imageID == "" || rev == 0 {
		return nil, nil
	}
	rows, err := m.Store.ListImageBuildsByRev(ctx, db.ListImageBuildsByRevParams{ImageID: imageID, Revision: rev})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if r.Status == "ready" {
			out = append(out, r.Arch)
		}
	}
	slices.Sort(out)
	return out, nil
}

// PrepareLayers ensures the custom image layer required by agent is cached and
// ref-bound on nodeID before Create or Rebuild executes on that node.
func (m *Manager) PrepareLayers(ctx context.Context, nodeID string, agent db.Agent) error {
	if agent.ImageID == "" || agent.ImageRevision == 0 {
		return nil
	}
	if m.Nodes == nil {
		return ErrNoBuildNode
	}
	host, arch, err := m.Nodes.LayerHost(ctx, nodeID)
	if err != nil {
		return err
	}
	b, err := m.Store.GetImageBuildByRevArch(ctx, db.GetImageBuildByRevArchParams{
		ImageID: agent.ImageID, Revision: agent.ImageRevision, Arch: arch,
	})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && b.Status != "ready") {
		return fmt.Errorf("image %s rev %d has no ready build for %s", agent.ImageID, agent.ImageRevision, arch)
	}
	if err != nil {
		return err
	}
	ref := LayerRef(agent.ImageID, agent.ImageRevision)
	ok, err := host.HasLayer(ctx, ref, b.LayerSha256)
	if err != nil {
		return fmt.Errorf("check layer cache on node %s: %w", nodeID, err)
	}
	if ok {
		return nil
	}
	f, err := m.layerStore().OpenZstd(b.LayerSha256)
	if err != nil {
		return fmt.Errorf("open stored layer %s: %w", b.LayerSha256, err)
	}
	defer f.Close()
	meta := node.LayerArtifact{
		Ref:        ref,
		SHA256:     b.LayerSha256,
		Size:       b.LayerSize,
		ZstdSHA256: b.ZstdSha256,
		ZstdSize:   b.ZstdSize,
		Subdir:     "upper",
	}
	if err := host.PutLayer(ctx, meta, f); err != nil {
		return fmt.Errorf("push layer to node %s: %w", nodeID, err)
	}
	return nil
}

func (m *Manager) verifySecrets(ctx context.Context, userID string, ids []string) error {
	for _, sid := range ids {
		s, err := m.Store.GetSecret(ctx, sid)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && s.OwnerID != userID) {
			return fmt.Errorf("%w: unknown secret %q", ErrValidation, sid)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) resolveBuildSecrets(ctx context.Context, userID string, ids []string) (*node.AgentConfig, map[string]string, error) {
	if len(ids) == 0 || m.Box == nil {
		return nil, nil, nil
	}
	var injections []secrets.Injection
	env := map[string]string{}
	for _, sid := range ids {
		s, err := m.Store.GetSecret(ctx, sid)
		if err != nil {
			return nil, nil, fmt.Errorf("load build secret %s: %w", sid, err)
		}
		if s.OwnerID != userID {
			return nil, nil, fmt.Errorf("build secret %s is not owned by the caller", sid)
		}
		var rule secrets.Rule
		if err := json.Unmarshal([]byte(s.Rule), &rule); err != nil {
			return nil, nil, fmt.Errorf("parse build secret %s rule: %w", s.Name, err)
		}
		val, err := m.Box.Open(s.Ciphertext, []byte(s.ID))
		if err != nil {
			return nil, nil, fmt.Errorf("decrypt build secret %s: %w", s.Name, err)
		}
		if envKeyRe.MatchString(s.Name) {
			env[s.Name] = s.Placeholder
		}
		injections = append(injections, secrets.Injection{
			SecretID:    s.ID,
			Rule:        rule,
			Value:       string(val),
			Placeholder: s.Placeholder,
		})
	}
	return &node.AgentConfig{Injections: injections}, env, nil
}

// FakeLayerBuilder returns a fast deterministic node.LayerBuilder for the fake
// sandbox driver (used in unit tests, API tests, and local UI development).
func FakeLayerBuilder(dataDir string) node.LayerBuilder {
	return func(ctx context.Context, req node.LayerBuildRequest, onLog func(line string)) (node.LayerBuildResult, io.ReadCloser, error) {
		if err := ctx.Err(); err != nil {
			return node.LayerBuildResult{}, nil, err
		}
		emit := func(s string) {
			if onLog != nil {
				onLog(s)
			}
		}
		emit("==> Booting simulated builder")
		if len(req.AptPackages) > 0 {
			emit("==> Installing APT packages: " + strings.Join(req.AptPackages, " "))
		}
		if strings.TrimSpace(req.SetupScript) != "" {
			emit("==> Running setup script")
			if strings.Contains(req.SetupScript, "__AGENTHOLD_FAIL_BUILD__") {
				return node.LayerBuildResult{}, nil, errors.New("build script exited with code 1")
			}
		}
		emit("==> Sealing and compressing image layer")
		payload, _ := json.Marshal(map[string]any{
			"imageId":     req.ImageID,
			"revision":    req.Revision,
			"aptPackages": req.AptPackages,
			"setupScript": req.SetupScript,
			"env":         req.Env,
		})
		rawSum := sha256.Sum256(payload)
		rawSHA := hex.EncodeToString(rawSum[:])

		var zbuf bytes.Buffer
		enc, err := zstd.NewWriter(&zbuf)
		if err != nil {
			return node.LayerBuildResult{}, nil, err
		}
		if _, err := enc.Write(payload); err != nil {
			_ = enc.Close()
			return node.LayerBuildResult{}, nil, err
		}
		if err := enc.Close(); err != nil {
			return node.LayerBuildResult{}, nil, err
		}
		zbytes := zbuf.Bytes()
		zSum := sha256.Sum256(zbytes)
		zSHA := hex.EncodeToString(zSum[:])

		meta := node.LayerArtifact{
			Ref:        LayerRef(req.ImageID, req.Revision),
			SHA256:     rawSHA,
			Size:       int64(len(payload)),
			ZstdSHA256: zSHA,
			ZstdSize:   int64(len(zbytes)),
			Subdir:     "upper",
		}
		if dataDir != "" {
			if err := node.NewLayerStore(dataDir).InstallFromZstd(meta, bytes.NewReader(zbytes)); err != nil {
				return node.LayerBuildResult{}, nil, err
			}
		}
		emit("==> Build finished")
		return node.LayerBuildResult{
			Arch:     runtime.GOARCH,
			BaseKey:  "fake-base",
			Artifact: meta,
		}, io.NopCloser(bytes.NewReader(zbytes)), nil
	}
}
