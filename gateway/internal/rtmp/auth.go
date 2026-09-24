package rtmp

import (
	"context"
	"github.com/google/uuid"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
)

// RepoAuthenticator adapts the LiveRepo to the Authenticator interface.
// Production wiring; tests substitute a stub.
type RepoAuthenticator struct {
	Live     *repo.LiveRepo
	Resolver interface {
		UpstreamURL(context.Context, uuid.UUID) (string, error)
	}
}

func (a *RepoAuthenticator) AuthenticateStreamKey(ctx context.Context, peppered string) (*AuthResult, error) {
	row, err := a.Live.FindActiveByStreamKeyHash(ctx, peppered)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	out := &AuthResult{
		LiveStreamID: row.ID.String(),
		APIKeyID:     row.APIKeyID.String(),
	}
	if a.Resolver == nil {
		return nil, nil
	}
	out.PrivateIngestURL, err = a.Resolver.UpstreamURL(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	if out.PrivateIngestURL == "" {
		return nil, nil
	}
	if row.BrokerSessionID != nil {
		out.BrokerSessionID = *row.BrokerSessionID
	}
	return out, nil
}
