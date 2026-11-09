// Package keyadmin provides an authorized application service for issuing and
// revoking controller API keys. Clients supply a trusted requester identity;
// the HTTP or local-OS boundary is responsible for establishing it.
package keyadmin

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/authkey"
)

const MaxKeyLifetime = 90 * 24 * time.Hour

var ErrUnauthorized = errors.New("API-key management is not authorized")

type Manager interface {
	Issue(authkey.IssueRequest) (authkey.IssuedKey, error)
	Revoke(id, revokedBy string, now time.Time) error
}

// Authorizer evaluates key administration against the authenticated requester
// and the complete requested scope set. Implementations must not trust an
// identity supplied by an untrusted client.
type Authorizer interface {
	AuthorizeIssue(context.Context, string, IssueRequest) error
	AuthorizeRevoke(context.Context, string, string) error
}

type IssueRequest struct {
	ActorID  string
	Audience string
	Scopes   []authkey.Scope
	Lifetime time.Duration
}

type Service struct {
	manager    Manager
	authorizer Authorizer
	clock      func() time.Time
}

func NewService(manager Manager, authorizer Authorizer, clock func() time.Time) (*Service, error) {
	if manager == nil || authorizer == nil {
		return nil, errors.New("API-key manager and authorizer are required")
	}
	if clock == nil {
		clock = time.Now
	}
	return &Service{manager: manager, authorizer: authorizer, clock: clock}, nil
}

func (s *Service) Issue(ctx context.Context, requesterID string, request IssueRequest) (authkey.IssuedKey, error) {
	if s == nil || s.manager == nil || s.authorizer == nil || ctx == nil || strings.TrimSpace(requesterID) == "" {
		return authkey.IssuedKey{}, ErrUnauthorized
	}
	if request.Lifetime < time.Minute || request.Lifetime > MaxKeyLifetime {
		return authkey.IssuedKey{}, errors.New("API-key lifetime must be between 1m and 90d")
	}
	if err := s.authorizer.AuthorizeIssue(ctx, requesterID, cloneIssueRequest(request)); err != nil {
		return authkey.IssuedKey{}, ErrUnauthorized
	}
	now := s.clock().UTC()
	return s.manager.Issue(authkey.IssueRequest{
		ActorID: request.ActorID, Audience: request.Audience,
		Scopes: append([]authkey.Scope(nil), request.Scopes...), CreatedBy: requesterID,
		CreatedAt: now, ExpiresAt: now.Add(request.Lifetime),
	})
}

func (s *Service) Revoke(ctx context.Context, requesterID, keyID string) error {
	if s == nil || s.manager == nil || s.authorizer == nil || ctx == nil || strings.TrimSpace(requesterID) == "" {
		return ErrUnauthorized
	}
	if err := s.authorizer.AuthorizeRevoke(ctx, requesterID, keyID); err != nil {
		return ErrUnauthorized
	}
	return s.manager.Revoke(keyID, requesterID, s.clock().UTC())
}

func cloneIssueRequest(request IssueRequest) IssueRequest {
	request.Scopes = append([]authkey.Scope(nil), request.Scopes...)
	return request
}
