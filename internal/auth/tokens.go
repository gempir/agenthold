package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store/db"
)

// TokenPrefix starts every personal access token.
const TokenPrefix = "ahp_"

// CreateToken issues a personal access token for userID. The token is only
// returned here; the database keeps its hash.
func (s *Service) CreateToken(ctx context.Context, userID, name string, expires *time.Time) (db.ApiToken, string, error) {
	token := TokenPrefix + id.Random(32)
	t := db.ApiToken{
		ID: id.New("tok"), UserID: userID, Name: name, TokenHash: HashToken(token),
		Prefix: token[:len(TokenPrefix)+6], CreatedAt: s.now().Unix(),
	}
	if expires != nil {
		t.ExpiresAt = sql.NullInt64{Int64: expires.Unix(), Valid: true}
	}
	err := s.store.CreateAPIToken(ctx, db.CreateAPITokenParams{
		ID: t.ID, UserID: t.UserID, Name: t.Name, TokenHash: t.TokenHash, Prefix: t.Prefix, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
	})
	return t, token, err
}

// TokenUser returns the user of a valid token.
func (s *Service) TokenUser(ctx context.Context, token string) (*db.User, error) {
	if !strings.HasPrefix(token, TokenPrefix) {
		return nil, ErrUnauthenticated
	}
	now := s.now()
	row, err := s.store.GetTokenUser(ctx, db.GetTokenUserParams{TokenHash: HashToken(token), ExpiresAt: sql.NullInt64{Int64: now.Unix(), Valid: true}})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	// Record use at most once a minute.
	if !row.LastUsedAt.Valid || now.Unix()-row.LastUsedAt.Int64 > 60 {
		_ = s.store.TouchAPIToken(ctx, db.TouchAPITokenParams{LastUsedAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, ID: row.TokenID})
	}
	return &row.User, nil
}
