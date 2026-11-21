package runtimeassets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
)

// roots lists the /dl/ roots to fetch assets from, best first:
//
//  1. Options.AssetsURL (explicit configuration),
//  2. the controller an edge node joined (asked afresh only now, when something is missing).
//
// Assets are named by key, not by release, so a controller on another version can serve them as
// long as it has the asset this node's release names.
func (o Options) roots(ctx context.Context) ([]string, error) {
	var roots []string
	if o.AssetsURL != "" {
		u, err := CheckRoot(o.AssetsURL)
		if err != nil {
			return nil, fmt.Errorf("sandbox.firecracker.assets_url: %w", err)
		}
		roots = append(roots, u)
	}
	if c := o.controller(ctx); c.DownloadURL != "" {
		u, err := CheckRoot(c.DownloadURL)
		switch {
		case err != nil && len(roots) == 0:
			return nil, fmt.Errorf("the controller's download URL is unusable: %w", err)
		case err != nil:
			log(o.Log).Warn("not fetching assets from the controller", "err", err)
		case !slices.Contains(roots, u):
			roots = append(roots, u)
		}
	}
	if len(roots) == 0 {
		return nil, errors.New("no source for the guest kernel and base image: set sandbox.firecracker.assets_url to the /dl/ root " +
			"of a host that serves this architecture's assets, or place them in the data directory (see the install docs)")
	}
	return roots, nil
}

// CheckRoot accepts the /dl/ root of an asset source: an absolute HTTP(S) URL without credentials,
// query or fragment, ending in "/".
func CheckRoot(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("%q must be an absolute HTTP(S) URL without credentials", raw)
	}
	if !strings.HasSuffix(u.Path, "/") {
		return "", fmt.Errorf("%q must be a /dl/ root ending in \"/\"", raw)
	}
	return raw, nil
}

// controller returns what is known about the node's controller, asking it afresh first when the
// caller provided a way to (only reached when a download is actually needed).
func (o Options) controller(ctx context.Context) Controller {
	c := o.Controller
	if c.Refresh == nil {
		return c
	}
	fresh, err := c.Refresh(ctx)
	if err != nil {
		log(o.Log).Info("could not ask the controller where it serves downloads from; using what was learned before", "err", err)
		return c
	}
	fresh.Live = true
	return fresh
}

func log(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l
}
