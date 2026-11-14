package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/version"
)

// GetVersion implements gen.StrictServerInterface.
func (a *API) GetVersion(context.Context, gen.GetVersionRequestObject) (gen.GetVersionResponseObject, error) {
	return gen.GetVersion200JSONResponse{Version: version.Version, Commit: version.Commit}, nil
}

// ListAuthProviders implements gen.StrictServerInterface.
func (a *API) ListAuthProviders(context.Context, gen.ListAuthProvidersRequestObject) (gen.ListAuthProvidersResponseObject, error) {
	out := gen.ListAuthProviders200JSONResponse{}
	for _, p := range a.Auth.Providers() {
		out = append(out, gen.AuthProvider{Id: p.ID, Name: p.Name, Type: gen.AuthProviderType(p.Type)})
	}
	return out, nil
}

// GetMe implements gen.StrictServerInterface.
func (a *API) GetMe(ctx context.Context, _ gen.GetMeRequestObject) (gen.GetMeResponseObject, error) {
	u := me(ctx)
	wsID, err := a.Store.GetPersonalWorkspaceID(ctx, u.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return gen.GetMe200JSONResponse{User: apiUser(*u), PersonalWorkspaceId: wsID}, nil
}

func apiWorkspace(w db.Workspace, role string) gen.Workspace {
	return gen.Workspace{
		Id: w.ID, Name: w.Name, Personal: w.Personal == 1,
		Role: gen.WorkspaceRole(role), CreatedAt: ts(w.CreatedAt),
	}
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", badRequest("name must be 1 to 100 characters")
	}
	return name, nil
}

// ListWorkspaces implements gen.StrictServerInterface.
func (a *API) ListWorkspaces(ctx context.Context, _ gen.ListWorkspacesRequestObject) (gen.ListWorkspacesResponseObject, error) {
	rows, err := a.Store.ListUserWorkspaces(ctx, me(ctx).ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListWorkspaces200JSONResponse{}
	for _, r := range rows {
		out = append(out, apiWorkspace(r.Workspace, r.Role))
	}
	return out, nil
}

// CreateWorkspace implements gen.StrictServerInterface.
func (a *API) CreateWorkspace(ctx context.Context, req gen.CreateWorkspaceRequestObject) (gen.CreateWorkspaceResponseObject, error) {
	name, err := validName(req.Body.Name)
	if err != nil {
		return nil, err
	}
	now := a.Now().Unix()
	ws := db.Workspace{ID: id.New("ws"), Name: name, CreatedAt: now, UpdatedAt: now}
	err = a.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateWorkspace(ctx, db.CreateWorkspaceParams{ID: ws.ID, Name: ws.Name, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		return q.UpsertMembership(ctx, db.UpsertMembershipParams{WorkspaceID: ws.ID, UserID: me(ctx).ID, Role: string(authz.WorkspaceOwner), CreatedAt: now})
	})
	if err != nil {
		return nil, err
	}
	return gen.CreateWorkspace201JSONResponse(apiWorkspace(ws, string(authz.WorkspaceOwner))), nil
}

// GetWorkspace implements gen.StrictServerInterface.
func (a *API) GetWorkspace(ctx context.Context, req gen.GetWorkspaceRequestObject) (gen.GetWorkspaceResponseObject, error) {
	role, err := a.authz.Workspace(ctx, me(ctx).ID, req.WorkspaceId, authz.WorkspaceRead)
	if err != nil {
		return nil, err
	}
	ws, err := a.Store.GetWorkspace(ctx, req.WorkspaceId)
	if err != nil {
		return nil, err
	}
	return gen.GetWorkspace200JSONResponse(apiWorkspace(ws, string(role))), nil
}

// UpdateWorkspace implements gen.StrictServerInterface.
func (a *API) UpdateWorkspace(ctx context.Context, req gen.UpdateWorkspaceRequestObject) (gen.UpdateWorkspaceResponseObject, error) {
	role, err := a.authz.Workspace(ctx, me(ctx).ID, req.WorkspaceId, authz.WorkspaceUpdate)
	if err != nil {
		return nil, err
	}
	name, err := validName(req.Body.Name)
	if err != nil {
		return nil, err
	}
	if err := a.Store.RenameWorkspace(ctx, db.RenameWorkspaceParams{Name: name, UpdatedAt: a.Now().Unix(), ID: req.WorkspaceId}); err != nil {
		return nil, err
	}
	ws, err := a.Store.GetWorkspace(ctx, req.WorkspaceId)
	if err != nil {
		return nil, err
	}
	return gen.UpdateWorkspace200JSONResponse(apiWorkspace(ws, string(role))), nil
}

// DeleteWorkspace implements gen.StrictServerInterface.
func (a *API) DeleteWorkspace(ctx context.Context, req gen.DeleteWorkspaceRequestObject) (gen.DeleteWorkspaceResponseObject, error) {
	if _, err := a.authz.Workspace(ctx, me(ctx).ID, req.WorkspaceId, authz.WorkspaceDelete); err != nil {
		return nil, err
	}
	ws, err := a.Store.GetWorkspace(ctx, req.WorkspaceId)
	if err != nil {
		return nil, err
	}
	if ws.Personal == 1 {
		return nil, authz.ErrPersonal
	}
	if err := a.Store.DeleteWorkspace(ctx, ws.ID); err != nil {
		return nil, err
	}
	return gen.DeleteWorkspace204Response{}, nil
}

func apiMember(r db.ListMembersRow) gen.Member {
	return gen.Member{User: apiUser(r.User), Role: gen.WorkspaceRole(r.Role), JoinedAt: ts(r.JoinedAt)}
}

// ListMembers implements gen.StrictServerInterface.
func (a *API) ListMembers(ctx context.Context, req gen.ListMembersRequestObject) (gen.ListMembersResponseObject, error) {
	if _, err := a.authz.Workspace(ctx, me(ctx).ID, req.WorkspaceId, authz.WorkspaceRead); err != nil {
		return nil, err
	}
	rows, err := a.Store.ListMembers(ctx, req.WorkspaceId)
	if err != nil {
		return nil, err
	}
	out := gen.ListMembers200JSONResponse{}
	for _, r := range rows {
		out = append(out, apiMember(r))
	}
	return out, nil
}

// memberChange loads what member management decisions need.
func (a *API) memberChange(ctx context.Context, q *db.Queries, wsID, targetID string) (actor, target authz.WorkspaceRole, owners int, err error) {
	chk := authz.Checker{Q: q}
	actor, err = chk.WorkspaceRoleOf(ctx, me(ctx).ID, wsID)
	if err != nil {
		return "", "", 0, err
	}
	if !actor.Valid() {
		return "", "", 0, authz.ErrNotFound
	}
	target, err = chk.WorkspaceRoleOf(ctx, targetID, wsID)
	if err != nil {
		return "", "", 0, err
	}
	if !target.Valid() {
		return "", "", 0, errNotFound
	}
	n, err := q.CountOwners(ctx, wsID)
	return actor, target, int(n), err
}

// UpdateMember implements gen.StrictServerInterface.
func (a *API) UpdateMember(ctx context.Context, req gen.UpdateMemberRequestObject) (gen.UpdateMemberResponseObject, error) {
	if !req.Body.Role.Valid() {
		return nil, badRequest("role must be owner, admin or member")
	}
	next := authz.WorkspaceRole(req.Body.Role)
	var out gen.Member
	err := a.Store.Tx(ctx, func(q *db.Queries) error {
		actor, current, owners, err := a.memberChange(ctx, q, req.WorkspaceId, req.UserId)
		if err != nil {
			return err
		}
		if err := authz.CanSetMemberRole(actor, current, next, owners); err != nil {
			return err
		}
		ws, err := q.GetWorkspace(ctx, req.WorkspaceId)
		if err != nil {
			return err
		}
		if ws.Personal == 1 && next == authz.WorkspaceOwner {
			return authz.ErrPersonal // a personal workspace has exactly one owner
		}
		m, err := q.GetMembership(ctx, db.GetMembershipParams{WorkspaceID: req.WorkspaceId, UserID: req.UserId})
		if err != nil {
			return err
		}
		if err := q.UpsertMembership(ctx, db.UpsertMembershipParams{WorkspaceID: m.WorkspaceID, UserID: m.UserID, Role: string(next), CreatedAt: m.CreatedAt}); err != nil {
			return err
		}
		u, err := q.GetUser(ctx, req.UserId)
		if err != nil {
			return err
		}
		out = apiMember(db.ListMembersRow{User: u, Role: string(next), JoinedAt: m.CreatedAt})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return gen.UpdateMember200JSONResponse(out), nil
}

// RemoveMember implements gen.StrictServerInterface.
func (a *API) RemoveMember(ctx context.Context, req gen.RemoveMemberRequestObject) (gen.RemoveMemberResponseObject, error) {
	err := a.Store.Tx(ctx, func(q *db.Queries) error {
		actor, target, owners, err := a.memberChange(ctx, q, req.WorkspaceId, req.UserId)
		if err != nil {
			return err
		}
		if err := authz.CanRemoveMember(actor, target, req.UserId == me(ctx).ID, owners); err != nil {
			return err
		}
		return q.DeleteMembership(ctx, db.DeleteMembershipParams{WorkspaceID: req.WorkspaceId, UserID: req.UserId})
	})
	if err != nil {
		return nil, err
	}
	return gen.RemoveMember204Response{}, nil
}

func apiInvite(i db.Invite) gen.Invite {
	return gen.Invite{
		Id: i.ID, Role: gen.InviteRole(i.Role), Email: i.Email, CreatedBy: i.CreatedBy,
		CreatedAt: ts(i.CreatedAt), ExpiresAt: ts(i.ExpiresAt),
	}
}

// ListInvites implements gen.StrictServerInterface.
func (a *API) ListInvites(ctx context.Context, req gen.ListInvitesRequestObject) (gen.ListInvitesResponseObject, error) {
	if _, err := a.authz.Workspace(ctx, me(ctx).ID, req.WorkspaceId, authz.InvitesManage); err != nil {
		return nil, err
	}
	rows, err := a.Store.ListPendingInvites(ctx, db.ListPendingInvitesParams{WorkspaceID: req.WorkspaceId, ExpiresAt: a.Now().Unix()})
	if err != nil {
		return nil, err
	}
	out := gen.ListInvites200JSONResponse{}
	for _, r := range rows {
		out = append(out, apiInvite(r))
	}
	return out, nil
}

// CreateInvite implements gen.StrictServerInterface.
func (a *API) CreateInvite(ctx context.Context, req gen.CreateInviteRequestObject) (gen.CreateInviteResponseObject, error) {
	actor, err := a.authz.Workspace(ctx, me(ctx).ID, req.WorkspaceId, authz.InvitesManage)
	if err != nil {
		return nil, err
	}
	if !req.Body.Role.Valid() {
		return nil, badRequest("role must be admin or member")
	}
	role := authz.WorkspaceRole(req.Body.Role)
	if err := authz.CanInvite(actor, role); err != nil {
		return nil, err
	}
	hours := 168
	if req.Body.ExpiresInHours != nil {
		hours = *req.Body.ExpiresInHours
		if hours < 1 || hours > 720 {
			return nil, badRequest("expiresInHours must be between 1 and 720")
		}
	}
	email := ""
	if req.Body.Email != nil {
		email = strings.TrimSpace(*req.Body.Email)
	}
	token := id.Random(24)
	now := a.Now()
	inv := db.Invite{
		ID: id.New("inv"), WorkspaceID: req.WorkspaceId, TokenHash: auth.HashToken(token), Role: string(role),
		Email: email, CreatedBy: me(ctx).ID, CreatedAt: now.Unix(), ExpiresAt: now.Add(time.Duration(hours) * time.Hour).Unix(),
	}
	if err := a.Store.CreateInvite(ctx, db.CreateInviteParams{
		ID: inv.ID, WorkspaceID: inv.WorkspaceID, TokenHash: inv.TokenHash, Role: inv.Role, Email: inv.Email,
		CreatedBy: inv.CreatedBy, CreatedAt: inv.CreatedAt, ExpiresAt: inv.ExpiresAt,
	}); err != nil {
		return nil, err
	}
	base := apiInvite(inv)
	return gen.CreateInvite201JSONResponse{
		Id: base.Id, Role: gen.CreatedInviteRole(base.Role), Email: base.Email, CreatedBy: base.CreatedBy,
		CreatedAt: base.CreatedAt, ExpiresAt: base.ExpiresAt, Token: token, Url: a.PublicURL + "/invite/" + token,
	}, nil
}

// RevokeInvite implements gen.StrictServerInterface.
func (a *API) RevokeInvite(ctx context.Context, req gen.RevokeInviteRequestObject) (gen.RevokeInviteResponseObject, error) {
	if _, err := a.authz.Workspace(ctx, me(ctx).ID, req.WorkspaceId, authz.InvitesManage); err != nil {
		return nil, err
	}
	n, err := a.Store.DeleteInvite(ctx, db.DeleteInviteParams{ID: req.InviteId, WorkspaceID: req.WorkspaceId})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errNotFound
	}
	return gen.RevokeInvite204Response{}, nil
}

// usableInvite returns the invite for a token if it is pending and unexpired.
func (a *API) usableInvite(ctx context.Context, q *db.Queries, token string) (db.Invite, error) {
	inv, err := q.GetInviteByToken(ctx, auth.HashToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return inv, errStatus(http.StatusNotFound, "invite not found or expired")
	}
	if err != nil {
		return inv, err
	}
	if inv.AcceptedAt.Valid || inv.ExpiresAt <= a.Now().Unix() {
		return inv, errStatus(http.StatusNotFound, "invite not found or expired")
	}
	return inv, nil
}

// GetInvite implements gen.StrictServerInterface.
func (a *API) GetInvite(ctx context.Context, req gen.GetInviteRequestObject) (gen.GetInviteResponseObject, error) {
	inv, err := a.usableInvite(ctx, a.Store.Queries, req.Token)
	if err != nil {
		return nil, err
	}
	ws, err := a.Store.GetWorkspace(ctx, inv.WorkspaceID)
	if err != nil {
		return nil, err
	}
	inviter, err := a.Store.GetUser(ctx, inv.CreatedBy)
	if err != nil {
		return nil, err
	}
	role, err := a.authz.WorkspaceRoleOf(ctx, me(ctx).ID, ws.ID)
	if err != nil {
		return nil, err
	}
	return gen.GetInvite200JSONResponse{
		WorkspaceId: ws.ID, WorkspaceName: ws.Name, Role: gen.InvitePreviewRole(inv.Role), InvitedBy: inviter.Name,
		ExpiresAt: ts(inv.ExpiresAt), AlreadyMember: role.Valid(),
	}, nil
}

// AcceptInvite implements gen.StrictServerInterface.
func (a *API) AcceptInvite(ctx context.Context, req gen.AcceptInviteRequestObject) (gen.AcceptInviteResponseObject, error) {
	u := me(ctx)
	var out gen.Workspace
	err := a.Store.Tx(ctx, func(q *db.Queries) error {
		inv, err := a.usableInvite(ctx, q, req.Token)
		if err != nil {
			return err
		}
		if inv.Email != "" && (u.EmailVerified != 1 || !strings.EqualFold(inv.Email, u.Email)) {
			return errStatus(http.StatusForbidden, "this invite is for a different (verified) email address")
		}
		current, err := authz.Checker{Q: q}.WorkspaceRoleOf(ctx, u.ID, inv.WorkspaceID)
		if err != nil {
			return err
		}
		if current.Valid() {
			return errStatus(http.StatusConflict, "you are already a member of this workspace")
		}
		now := a.Now().Unix()
		n, err := q.MarkInviteAccepted(ctx, db.MarkInviteAcceptedParams{
			AcceptedBy: sql.NullString{String: u.ID, Valid: true}, AcceptedAt: sql.NullInt64{Int64: now, Valid: true}, ID: inv.ID,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return errStatus(http.StatusNotFound, "invite not found or expired")
		}
		if err := q.UpsertMembership(ctx, db.UpsertMembershipParams{WorkspaceID: inv.WorkspaceID, UserID: u.ID, Role: inv.Role, CreatedAt: now}); err != nil {
			return err
		}
		ws, err := q.GetWorkspace(ctx, inv.WorkspaceID)
		if err != nil {
			return err
		}
		out = apiWorkspace(ws, inv.Role)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return gen.AcceptInvite200JSONResponse(out), nil
}
