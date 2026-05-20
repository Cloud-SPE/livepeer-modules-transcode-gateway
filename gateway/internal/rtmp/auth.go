package rtmp

import (
	"context"

	"github.com/Cloud-SPE/livepeer-modules-transcode-gateway/gateway/internal/repo"
)

// RepoAuthenticator adapts the LiveRepo to the Authenticator interface.
// Production wiring; tests substitute a stub.
type RepoAuthenticator struct {
	Live *repo.LiveRepo
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
	if row.PrivateIngestURL != nil {
		out.PrivateIngestURL = *row.PrivateIngestURL
	}
	if row.BrokerSessionID != nil {
		out.BrokerSessionID = *row.BrokerSessionID
	}
	return out, nil
}
