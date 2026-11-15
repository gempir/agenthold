package api

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/version"
)

// GetVersion implements gen.StrictServerInterface.
func (a *API) GetVersion(context.Context, gen.GetVersionRequestObject) (gen.GetVersionResponseObject, error) {
	return gen.GetVersion200JSONResponse{Version: version.Version, Commit: version.Commit}, nil
}

// ListAuthProviders implements gen.StrictServerInterface. An empty list
// means nobody can sign in (the login page says so).
func (a *API) ListAuthProviders(context.Context, gen.ListAuthProvidersRequestObject) (gen.ListAuthProvidersResponseObject, error) {
	out := gen.ListAuthProviders200JSONResponse{}
	if a.Auth.LocalEnabled() {
		out = append(out, gen.AuthProvider{Id: config.LocalProviderID, Name: "Username and password", Type: gen.Local})
	}
	for _, p := range a.Auth.Providers() {
		out = append(out, gen.AuthProvider{Id: p.ID, Name: p.Name, Type: gen.AuthProviderType(p.Type)})
	}
	return out, nil
}

// GetMe implements gen.StrictServerInterface.
func (a *API) GetMe(ctx context.Context, _ gen.GetMeRequestObject) (gen.GetMeResponseObject, error) {
	return gen.GetMe200JSONResponse{User: apiUser(*me(ctx))}, nil
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", badRequest("name must be 1 to 100 characters")
	}
	return name, nil
}
