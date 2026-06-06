package loc

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// CreateJob mints a one-shot job: LOC selects the route, encumbers the
// worst-case credit, and returns the payment envelope + broker URL. The
// caller makes the broker call itself and MUST eventually settle —
// settle(actual_units=0) on failure to release the encumbrance.
//
// Deliberately NOT retried: LOC jobs have no idempotency keys, so a
// retry after an ambiguous timeout could double-encumber credit. A
// clean 429 is surfaced for the caller to decide.
func (c *Client) CreateJob(ctx context.Context, in CreateJobRequest) (*CreateJobResponse, error) {
	var out CreateJobResponse
	if err := c.doJSON(ctx, http.MethodPost, "/v1/jobs", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SettleJob reports actual consumed units, releasing funded − billed
// back to the credit pool. Retries 429/5xx/transport errors (a
// duplicate settle returns 409 job_already_settled — callers should
// treat that as success via IsAlreadySettled).
func (c *Client) SettleJob(ctx context.Context, jobID uuid.UUID, in SettleJobRequest) (*SettleJobResponse, error) {
	var out SettleJobResponse
	if err := c.doJSONRetry(ctx, http.MethodPost, "/v1/jobs/"+jobID.String()+"/settle", in, &out, 3); err != nil {
		return nil, err
	}
	return &out, nil
}
