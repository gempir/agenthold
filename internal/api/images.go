package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/images"
	"github.com/gempir/agenthold/internal/store/db"
)

func apiCustomImage(s images.ImageSummary, isAdmin bool) gen.CustomImage {
	secretIDs := s.SecretIDs
	if !isAdmin || secretIDs == nil {
		secretIDs = []string{}
	}
	readyArches := s.ReadyArches
	if readyArches == nil {
		readyArches = []string{}
	}
	apt := s.AptPackages
	if apt == nil {
		apt = []string{}
	}
	env := s.Env
	if env == nil {
		env = map[string]string{}
	}
	setupScript := s.Row.SetupScript
	if !isAdmin {
		setupScript = ""
	}
	out := gen.CustomImage{
		Id:             s.Row.ID,
		Name:           s.Row.Name,
		Description:    s.Row.Description,
		AptPackages:    apt,
		SetupScript:    setupScript,
		Env:            env,
		SecretIds:      secretIDs,
		IsDefault:      s.Row.IsDefault == 1,
		LatestRevision: s.Row.LatestRev,
		ReadyArches:    readyArches,
		Building:       s.Building,
		AgentCount:     s.AgentCount,
		CreatedAt:      ts(s.Row.CreatedAt),
		UpdatedAt:      ts(s.Row.UpdatedAt),
	}
	if s.LatestBuild != nil {
		st := gen.CustomImageLatestBuildStatus(s.LatestBuild.Status)
		out.LatestBuildStatus = &st
	}
	return out
}

func apiImageBuild(b db.ImageBuild) gen.ImageBuild {
	out := gen.ImageBuild{
		Id:          b.ID,
		ImageId:     b.ImageID,
		Revision:    b.Revision,
		Arch:        b.Arch,
		NodeId:      b.NodeID,
		Status:      gen.ImageBuildStatus(b.Status),
		Error:       b.Error,
		LayerSha256: b.LayerSha256,
		LayerSize:   b.LayerSize,
		ZstdSize:    b.ZstdSize,
		CreatedAt:   ts(b.CreatedAt),
	}
	if b.BaseKey != "" {
		bk := b.BaseKey
		out.BaseKey = &bk
	}
	if b.StartedAt > 0 {
		t := ts(b.StartedAt)
		out.StartedAt = &t
	}
	if b.FinishedAt > 0 {
		t := ts(b.FinishedAt)
		out.FinishedAt = &t
	}
	return out
}

func imageInputToRecipe(in gen.ImageInput) images.Recipe {
	r := images.Recipe{
		Name: in.Name,
	}
	if in.Description != nil {
		r.Description = *in.Description
	}
	if in.AptPackages != nil {
		r.AptPackages = *in.AptPackages
	}
	if in.SetupScript != nil {
		r.SetupScript = *in.SetupScript
	}
	if in.Env != nil {
		r.Env = *in.Env
	}
	if in.SecretIds != nil {
		r.SecretIDs = *in.SecretIds
	}
	r.IsDefault = in.IsDefault
	return r
}

func mapImageErr(err error) error {
	switch {
	case errors.Is(err, images.ErrNotFound), errors.Is(err, images.ErrBuildNotFound):
		return errNotFound
	case errors.Is(err, images.ErrValidation):
		return badRequest(err.Error())
	case errors.Is(err, images.ErrNameTaken), errors.Is(err, images.ErrImageInUse), errors.Is(err, images.ErrNotBuilt), errors.Is(err, images.ErrNoBuildNode):
		return errStatus(http.StatusConflict, err.Error())
	default:
		return err
	}
}

// ListImages implements gen.StrictServerInterface.
func (a *API) ListImages(ctx context.Context, _ gen.ListImagesRequestObject) (gen.ListImagesResponseObject, error) {
	u := me(ctx)
	isAdmin := u != nil && auth.IsAdmin(*u)
	baseDefault := true
	list := []gen.CustomImage{}
	if a.Images != nil {
		items, err := a.Images.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.Row.IsDefault == 1 {
				baseDefault = false
			}
			list = append(list, apiCustomImage(item, isAdmin))
		}
	}
	return gen.ListImages200JSONResponse{
		Base: gen.BuiltinBaseImage{
			Id:          images.BuiltinBaseID,
			Name:        images.BuiltinBaseName,
			Description: "Debian trixie with Node.js 24, Python 3, Chromium, git, build-essential, and preinstalled coding harnesses.",
			IsDefault:   baseDefault,
		},
		Images: list,
	}, nil
}

// CreateImage implements gen.StrictServerInterface.
func (a *API) CreateImage(ctx context.Context, req gen.CreateImageRequestObject) (gen.CreateImageResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if a.Images == nil {
		return nil, errStatus(http.StatusConflict, "custom images are not configured on this server")
	}
	img, err := a.Images.Create(ctx, me(ctx).ID, imageInputToRecipe(*req.Body))
	if err != nil {
		return nil, mapImageErr(err)
	}
	return gen.CreateImage201JSONResponse(apiCustomImage(img, true)), nil
}

// GetImage implements gen.StrictServerInterface.
func (a *API) GetImage(ctx context.Context, req gen.GetImageRequestObject) (gen.GetImageResponseObject, error) {
	if a.Images == nil {
		return nil, errNotFound
	}
	u := me(ctx)
	isAdmin := u != nil && auth.IsAdmin(*u)
	img, err := a.Images.Get(ctx, req.ImageId)
	if err != nil {
		return nil, mapImageErr(err)
	}
	builds := []gen.ImageBuild{}
	if isAdmin {
		rows, err := a.Images.ListBuilds(ctx, req.ImageId)
		if err != nil {
			return nil, mapImageErr(err)
		}
		for _, r := range rows {
			builds = append(builds, apiImageBuild(r))
		}
	}
	ci := apiCustomImage(img, isAdmin)
	var latestStatus *gen.CustomImageDetailLatestBuildStatus
	if ci.LatestBuildStatus != nil {
		s := gen.CustomImageDetailLatestBuildStatus(*ci.LatestBuildStatus)
		latestStatus = &s
	}
	return gen.GetImage200JSONResponse{
		Id:                ci.Id,
		Name:              ci.Name,
		Description:       ci.Description,
		AptPackages:       ci.AptPackages,
		SetupScript:       ci.SetupScript,
		Env:               ci.Env,
		SecretIds:         ci.SecretIds,
		IsDefault:         ci.IsDefault,
		LatestRevision:    ci.LatestRevision,
		ReadyArches:       ci.ReadyArches,
		Building:          ci.Building,
		AgentCount:        ci.AgentCount,
		LatestBuildStatus: latestStatus,
		CreatedAt:         ci.CreatedAt,
		UpdatedAt:         ci.UpdatedAt,
		Builds:            builds,
	}, nil
}

// UpdateImage implements gen.StrictServerInterface.
func (a *API) UpdateImage(ctx context.Context, req gen.UpdateImageRequestObject) (gen.UpdateImageResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if a.Images == nil {
		return nil, errNotFound
	}
	img, err := a.Images.Update(ctx, me(ctx).ID, req.ImageId, imageInputToRecipe(*req.Body))
	if err != nil {
		return nil, mapImageErr(err)
	}
	return gen.UpdateImage200JSONResponse(apiCustomImage(img, true)), nil
}

// DeleteImage implements gen.StrictServerInterface.
func (a *API) DeleteImage(ctx context.Context, req gen.DeleteImageRequestObject) (gen.DeleteImageResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if a.Images == nil {
		return nil, errNotFound
	}
	if err := a.Images.Delete(ctx, req.ImageId); err != nil {
		return nil, mapImageErr(err)
	}
	return gen.DeleteImage204Response{}, nil
}

// ListImageBuilds implements gen.StrictServerInterface.
func (a *API) ListImageBuilds(ctx context.Context, req gen.ListImageBuildsRequestObject) (gen.ListImageBuildsResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if a.Images == nil {
		return nil, errNotFound
	}
	rows, err := a.Images.ListBuilds(ctx, req.ImageId)
	if err != nil {
		return nil, mapImageErr(err)
	}
	out := make(gen.ListImageBuilds200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, apiImageBuild(r))
	}
	return out, nil
}

// StartImageBuild implements gen.StrictServerInterface.
func (a *API) StartImageBuild(ctx context.Context, req gen.StartImageBuildRequestObject) (gen.StartImageBuildResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if a.Images == nil {
		return nil, errStatus(http.StatusConflict, "custom images are not configured on this server")
	}
	rows, err := a.Images.StartBuild(ctx, me(ctx).ID, req.ImageId)
	if err != nil {
		return nil, mapImageErr(err)
	}
	out := make(gen.StartImageBuild202JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, apiImageBuild(r))
	}
	return out, nil
}

// GetImageBuild implements gen.StrictServerInterface.
func (a *API) GetImageBuild(ctx context.Context, req gen.GetImageBuildRequestObject) (gen.GetImageBuildResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if a.Images == nil {
		return nil, errNotFound
	}
	b, err := a.Images.GetBuild(ctx, req.ImageId, req.BuildId)
	if err != nil {
		return nil, mapImageErr(err)
	}
	base := apiImageBuild(b)
	return gen.GetImageBuild200JSONResponse{
		Id:          base.Id,
		ImageId:     base.ImageId,
		Revision:    base.Revision,
		Arch:        base.Arch,
		NodeId:      base.NodeId,
		Status:      gen.ImageBuildDetailStatus(base.Status),
		Error:       base.Error,
		BaseKey:     base.BaseKey,
		LayerSha256: base.LayerSha256,
		LayerSize:   base.LayerSize,
		ZstdSize:    base.ZstdSize,
		StartedAt:   base.StartedAt,
		FinishedAt:  base.FinishedAt,
		CreatedAt:   base.CreatedAt,
		Log:         b.LogText,
	}, nil
}

type machineImageMeta struct {
	id              string
	name            string
	revision        int64
	updateAvailable bool
}

func (a *API) machineImageMeta(ctx context.Context, ag db.Agent) machineImageMeta {
	if ag.ImageID == "" {
		return machineImageMeta{id: "", name: images.BuiltinBaseName, revision: 0}
	}
	meta := machineImageMeta{id: ag.ImageID, name: ag.ImageID, revision: ag.ImageRevision}
	if row, err := a.Store.GetImage(ctx, ag.ImageID); err == nil {
		meta.name = row.Name
		if row.LatestRev > ag.ImageRevision {
			meta.updateAvailable = true
		}
	}
	return meta
}

// resolveMachineImagePatch computes the new (imageID, revision, changed) when
// PATCH /agents/{id} or PATCH /apps/{id} includes imageId or imageRevision.
func (a *API) resolveMachineImagePatch(ctx context.Context, ag db.Agent, patchImageID *string, patchRev *int64) (string, int64, bool, error) {
	if patchImageID == nil && patchRev == nil {
		return ag.ImageID, ag.ImageRevision, false, nil
	}
	if a.Images == nil {
		return "", 0, false, badRequest("custom images are not configured on this server")
	}
	targetID := ag.ImageID
	if patchImageID != nil {
		v := strings.TrimSpace(*patchImageID)
		if v == "" || v == images.BuiltinBaseID {
			return "", 0, ag.ImageID != "" || ag.ImageRevision != 0, nil
		}
		targetID = v
	}
	if targetID == "" {
		return "", 0, false, badRequest("cannot set imageRevision on the built-in base image")
	}
	img, err := a.Images.Get(ctx, targetID)
	if err != nil {
		return "", 0, false, mapImageErr(err)
	}
	targetRev := img.Row.LatestRev
	if patchRev != nil && *patchRev > 0 {
		targetRev = *patchRev
	}
	if targetRev == 0 {
		return "", 0, false, mapImageErr(images.ErrNotBuilt)
	}
	arches, err := a.Images.ReadyArchesFor(ctx, targetID, targetRev)
	if err != nil {
		return "", 0, false, err
	}
	if len(arches) == 0 {
		return "", 0, false, errStatus(http.StatusConflict, "the requested image revision has no ready build")
	}
	changed := targetID != ag.ImageID || targetRev != ag.ImageRevision
	return targetID, targetRev, changed, nil
}
