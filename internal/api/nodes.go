package api

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/version"
)

// NodeAdmin manages the controller's nodes (node.Cluster).
type NodeAdmin interface {
	Nodes(ctx context.Context) ([]node.Info, error)
	CreateJoinToken(ctx context.Context, createdBy, name string, ttl time.Duration) (node.JoinToken, error)
	RemoveNode(ctx context.Context, id string, force bool) error
}

func (a *API) nodeInfos(ctx context.Context) ([]node.Info, error) {
	if a.Nodes != nil {
		return a.Nodes.Nodes(ctx)
	}
	// A bare in-process node (tests, development tools) is always connected.
	return []node.Info{{ID: node.LocalID, Name: node.LocalID, Local: true, Connected: true, Version: version.Version}}, nil
}

// requireAdmin allows only users who manage the installation.
func (a *API) requireAdmin(ctx context.Context) error {
	u := me(ctx)
	if u == nil {
		return errUnauthorized
	}
	if !auth.IsAdmin(*u) {
		return errStatus(http.StatusForbidden, "this is for admins only")
	}
	return nil
}

func apiNode(info node.Info) gen.NodeInfo {
	out := gen.NodeInfo{Id: info.ID, Name: info.Name, Online: info.Connected, Local: info.Local, Agents: info.Agents}
	if !info.LastSeen.IsZero() {
		seen := info.LastSeen
		out.LastSeen = &seen
	}
	if info.Capacity.CPUs > 0 {
		cpus := info.Capacity.CPUs
		memory := int64(min(info.Capacity.MemoryBytes, math.MaxInt64))
		disk := int64(min(info.Capacity.DiskBytes, math.MaxInt64))
		out.Cpus, out.AvailableMemoryBytes, out.FreeDiskBytes = &cpus, &memory, &disk
	}
	return out
}

// ListNodes implements gen.StrictServerInterface.
func (a *API) ListNodes(ctx context.Context, _ gen.ListNodesRequestObject) (gen.ListNodesResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	infos, err := a.nodeInfos(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.ListNodes200JSONResponse{}
	for _, info := range infos {
		out = append(out, apiNode(info))
	}
	return out, nil
}

// CreateNodeJoinToken implements gen.StrictServerInterface.
func (a *API) CreateNodeJoinToken(ctx context.Context, req gen.CreateNodeJoinTokenRequestObject) (gen.CreateNodeJoinTokenResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	ttl := time.Hour
	name := ""
	if req.Body != nil {
		if m := req.Body.TtlMinutes; m != nil {
			if *m < 1 || *m > 24*60 {
				return nil, badRequest("ttlMinutes must be between 1 and 1440")
			}
			ttl = time.Duration(*m) * time.Minute
		}
		if req.Body.Name != nil && strings.TrimSpace(*req.Body.Name) != "" {
			n, err := validName(*req.Body.Name)
			if err != nil {
				return nil, err
			}
			name = n
		}
	}
	if a.Nodes == nil {
		return nil, errStatus(http.StatusConflict, "edge nodes are not available on this server")
	}
	tok, err := a.Nodes.CreateJoinToken(ctx, me(ctx).ID, name, ttl)
	if err != nil {
		if strings.Contains(err.Error(), "disabled") {
			return nil, errStatus(http.StatusConflict, err.Error())
		}
		return nil, err
	}
	return gen.CreateNodeJoinToken201JSONResponse{
		Token: tok.Token, ExpiresAt: tok.ExpiresAt, Command: "agenthold node --join " + tok.Token,
	}, nil
}

// DeleteNode implements gen.StrictServerInterface.
func (a *API) DeleteNode(ctx context.Context, req gen.DeleteNodeRequestObject) (gen.DeleteNodeResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if a.Nodes == nil {
		return nil, errNotFound
	}
	force := req.Params.Force != nil && *req.Params.Force
	switch err := a.Nodes.RemoveNode(ctx, req.NodeId, force); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, errNotFound
	case errors.Is(err, node.ErrNodeInUse):
		return nil, errStatus(http.StatusConflict, err.Error())
	case err != nil && req.NodeId == node.LocalID:
		return nil, errStatus(http.StatusConflict, err.Error())
	case err != nil:
		return nil, err
	}
	return gen.DeleteNode204Response{}, nil
}

// nodeByID finds an agent's node. An empty ID is the built-in node.
func nodeByID(infos []node.Info, id string) node.Info {
	if id == "" {
		id = node.LocalID
	}
	for _, info := range infos {
		if info.ID == id {
			return info
		}
	}
	return node.Info{ID: id, Name: id}
}
