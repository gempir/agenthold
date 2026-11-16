package runtimeassets

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"runtime"
	"strings"
)

// BaseFileName is the name a release's compressed base image has under a /dl/ root.
func BaseFileName(arch string) string { return "base-" + arch + ".ext4.zst" }

// candidates lists where to try fetching the base, best first:
//
//  1. Options.BaseURL (explicit configuration; the only candidate when set),
//  2. the controller an edge node joined, if it runs this node's version,
//  3. the URL embedded in the manifest, if it is absolute.
func (o Options) candidates(m manifest) ([]string, error) {
	if o.BaseURL != "" {
		u, err := resolveRoot(o.BaseURL, m)
		if err != nil {
			return nil, fmt.Errorf("sandbox.firecracker.base_url: %w", err)
		}
		return []string{u}, nil
	}
	var urls []string
	var skew error
	if o.Controller.DownloadURL != "" {
		if o.Controller.Version != m.Version {
			skew = fmt.Errorf("this node runs agenthold %s but its controller runs %s, so the controller cannot serve this node's base image: "+
				"upgrade whichever side is older so both run the same version, or set sandbox.firecracker.base_url",
				m.Version, o.Controller.Version)
		} else if u, err := resolveRoot(o.Controller.DownloadURL, m); err != nil {
			return nil, fmt.Errorf("controller download URL: %w", err)
		} else {
			urls = append(urls, u)
		}
	}
	if u, err := url.Parse(m.Base.URL); err == nil && u.Host != "" {
		if skew != nil {
			log(o.Log).Warn("not fetching the base image from the controller", "err", skew)
		}
		urls = append(urls, m.Base.URL)
	}
	if len(urls) == 0 {
		if skew != nil {
			return nil, skew
		}
		return nil, errors.New("this release names no download location for its base image: set sandbox.firecracker.base_url " +
			"to a controller's /dl/ root (for example https://demo.agenthold.dev/dl/) or the compressed image's URL, " +
			"or place base/<sha256>.ext4 or base/<sha256>.ext4.zst in the data directory")
	}
	return urls, nil
}

// resolveRoot turns a configured location into the URL of the compressed base: a URL ending in "/"
// is a /dl/ root and gets the release's file name (or the manifest's relative base URL) appended;
// anything else already names the file.
func resolveRoot(raw string, m manifest) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("%q must be an absolute HTTP(S) URL without credentials", raw)
	}
	if !strings.HasSuffix(u.Path, "/") {
		return raw, nil
	}
	rel := m.Version + "/" + BaseFileName(runtime.GOARCH)
	if ru, err := url.Parse(m.Base.URL); err == nil && m.Base.URL != "" && ru.Host == "" && ru.Scheme == "" {
		rel = m.Base.URL
	}
	return u.ResolveReference(&url.URL{Path: strings.TrimLeft(rel, "/")}).String(), nil
}

func log(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l
}
