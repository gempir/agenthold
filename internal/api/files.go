package api

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strconv"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

const maxUpload = 1 << 30 // 1 GiB

// connected returns the agent's tunnel session after an authorization check.
// File operations do not wake sleeping agents.
func (a *API) connected(ctx context.Context, agentID string, action authz.Action) (*tunnel.Session, error) {
	s, _, err := a.connectedAgent(ctx, agentID, action)
	return s, err
}

func (a *API) connectedAgent(ctx context.Context, agentID string, action authz.Action) (*tunnel.Session, db.Agent, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, agentID, action)
	if err != nil {
		return nil, db.Agent{}, err
	}
	s, err := a.session(ctx, agentID)
	return s, acc.Row.Agent, err
}

func (a *API) session(ctx context.Context, agentID string) (*tunnel.Session, error) {
	ok, err := a.Node.Connected(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if !ok {
		if err := a.Sandboxes.Reachable(ctx, agentID); err != nil {
			return nil, err
		}
		return nil, tunnel.ErrNotConnected
	}
	if a.Idle != nil {
		a.Idle.Tracker.Touch(agentID)
	}
	return node.Client(a.Node, agentID), nil
}

func requirePath(p *string) (string, error) {
	if p == nil || *p == "" {
		return "", badRequest("path is required")
	}
	return *p, nil
}

// ListFiles implements gen.StrictServerInterface.
func (a *API) ListFiles(ctx context.Context, req gen.ListFilesRequestObject) (gen.ListFilesResponseObject, error) {
	s, err := a.connected(ctx, req.AgentId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	p := ""
	if req.Params.Path != nil {
		p = *req.Params.Path
	}
	ls, err := s.List(ctx, p)
	if err != nil {
		return nil, err
	}
	out := gen.ListFiles200JSONResponse{Path: ls.Path, Entries: make([]gen.FileEntry, 0, len(ls.Entries))}
	for _, e := range ls.Entries {
		out.Entries = append(out.Entries, gen.FileEntry{
			Name: e.Name, Size: e.Size, Mode: e.Mode, IsDir: e.IsDir, IsLink: e.IsLink, ModTime: ts(e.ModTime),
		})
	}
	return out, nil
}

// DownloadFile implements gen.StrictServerInterface. The file is streamed:
// the response starts once sandboxd reports the size.
func (a *API) DownloadFile(ctx context.Context, req gen.DownloadFileRequestObject) (gen.DownloadFileResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	s, err := a.session(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	// Users who cannot open a terminal must not read harness logins and
	// sandboxd's boot file through downloads. (Everyone with a terminal can
	// read them there; such agents are marked credentialsInSandbox.)
	var deny []string
	if !authz.Can(acc.Role, authz.AgentTerminal) {
		deny = append(deny, "/etc/agenthold")
		if ad, ok, _ := a.resolveAdapter(ctx, acc.Row.Agent.Harness); ok {
			for _, p := range ad.CredentialPaths {
				deny = append(deny, "~/"+p)
			}
		}
	}
	pr, pw := ioPipe()
	sizeCh := make(chan int64, 1)
	errCh := make(chan error, 1)
	go func() {
		err := s.ReadFile(ctx, req.Params.Path, func(n int64) { sizeCh <- n }, pw, deny...)
		_ = pw.CloseWithError(err)
		errCh <- err
	}()
	select {
	case n := <-sizeCh:
		return downloadResponse{name: path.Base(req.Params.Path), size: n, body: pr}, nil
	case err := <-errCh:
		if err == nil {
			err = errors.New("no data")
		}
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type downloadResponse struct {
	name string
	size int64
	body interface{ Read([]byte) (int, error) }
}

func (d downloadResponse) VisitDownloadFileResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(d.size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(d.name)+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, err := copyBuffer(w, d.body)
	return err
}

func sanitizeFilename(n string) string {
	out := make([]rune, 0, len(n))
	for _, r := range n {
		if r < 0x20 || r == '"' || r == '\\' || r > 0x7e {
			r = '_'
		}
		out = append(out, r)
	}
	return string(out)
}

// UploadFile implements gen.StrictServerInterface.
func (a *API) UploadFile(ctx context.Context, req gen.UploadFileRequestObject) (gen.UploadFileResponseObject, error) {
	s, ag, err := a.connectedAgent(ctx, req.AgentId, authz.AgentFilesWrite)
	if err != nil {
		return nil, err
	}
	if req.Params.Path == "" {
		return nil, badRequest("path is required")
	}
	if err := s.WriteFile(ctx, req.Params.Path, 0o644, limitReader(req.Body, maxUpload)); err != nil {
		return nil, err
	}
	a.record(ctx, ag, audit.FileWrite, req.Params.Path, nil)
	return gen.UploadFile204Response{}, nil
}

// MakeDirectory implements gen.StrictServerInterface.
func (a *API) MakeDirectory(ctx context.Context, req gen.MakeDirectoryRequestObject) (gen.MakeDirectoryResponseObject, error) {
	s, ag, err := a.connectedAgent(ctx, req.AgentId, authz.AgentFilesWrite)
	if err != nil {
		return nil, err
	}
	p, err := requirePath(&req.Body.Path)
	if err != nil {
		return nil, err
	}
	if err := s.Mkdir(ctx, p, true); err != nil {
		return nil, err
	}
	a.record(ctx, ag, audit.FileMkdir, p, nil)
	return gen.MakeDirectory204Response{}, nil
}

// DeleteFile implements gen.StrictServerInterface.
func (a *API) DeleteFile(ctx context.Context, req gen.DeleteFileRequestObject) (gen.DeleteFileResponseObject, error) {
	s, ag, err := a.connectedAgent(ctx, req.AgentId, authz.AgentFilesWrite)
	if err != nil {
		return nil, err
	}
	if req.Params.Path == "" {
		return nil, badRequest("path is required")
	}
	if err := s.Remove(ctx, req.Params.Path, req.Params.Recursive != nil && *req.Params.Recursive); err != nil {
		return nil, err
	}
	a.record(ctx, ag, audit.FileDelete, req.Params.Path, nil)
	return gen.DeleteFile204Response{}, nil
}
