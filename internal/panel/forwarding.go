package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/P0me1oo/YZ-Agent/internal/portforward"
)

// A missing endpoint means an older panel; it must not erase persisted state.
var ErrForwardingUnauthorized = errors.New("forwarding authorization revoked")

var ErrForwardingUnsupported = errors.New("panel does not support server forwarding")

func (c *Client) ExchangeForwarding(ctx context.Context, status []portforward.Status) (portforward.Policy, error) {
	payload := map[string]any{"forwarding_status": status}
	c.injectAuth(payload)
	body, err := json.Marshal(payload)
	if err != nil {
		return portforward.Policy{}, err
	}
	response, err := c.doRequestContext(ctx, http.MethodPost, "/api/v2/server/machine/forwarding", body, "")
	if err != nil {
		return portforward.Policy{}, err
	}
	defer drainAndClose(response.Body)
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return portforward.Policy{}, ErrForwardingUnauthorized
	}
	if response.StatusCode == http.StatusNotFound {
		return portforward.Policy{}, ErrForwardingUnsupported
	}
	if response.StatusCode != http.StatusOK {
		return portforward.Policy{}, errors.New("forwarding policy request failed")
	}
	var p portforward.Policy
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&p); err != nil {
		return p, err
	}
	return p, p.Validate()
}
