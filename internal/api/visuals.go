package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/session"
)

// visualCSP strictly sandboxes direct visual HTML responses from the API so
// opening a visual in a standalone tab cannot access the agenthold origin,
// cookies, or local-network endpoints.
const visualCSP = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' https:; style-src 'unsafe-inline' https:; img-src data: blob: https:; font-src data: https:; media-src data: blob: https:; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"

// GetSessionVisual implements gen.StrictServerInterface.
func (a *API) GetSessionVisual(ctx context.Context, req gen.GetSessionVisualRequestObject) (gen.GetSessionVisualResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	doc, err := a.Sessions.GetVisualEvent(ctx, s.ID, req.EventSeq)
	if err != nil {
		if errors.Is(err, session.ErrEventNotFound) {
			return nil, errNotFound
		}
		return nil, err
	}
	download := req.Params.Download != nil && *req.Params.Download
	return visualHTMLResponse{doc: doc, download: download}, nil
}

type visualHTMLResponse struct {
	doc      session.VisualDocument
	download bool
}

func (r visualHTMLResponse) VisitGetSessionVisualResponse(w http.ResponseWriter) error {
	body := []byte(r.doc.HTML)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Content-Security-Policy", visualCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Cache-Control", "private, max-age=300")
	if r.download {
		name := visualDownloadName(r.doc.Title, r.doc.Seq)
		w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(name)+`"`)
	}
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(body)
	return err
}

func visualDownloadName(title string, seq int64) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
		if b.Len() >= 60 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "visual-" + strconv.FormatInt(seq, 10)
	}
	return slug + ".html"
}
