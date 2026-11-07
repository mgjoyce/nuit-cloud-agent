package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrTooLarge is returned when the platform's answer is bigger than the limit the caller set.
var ErrTooLarge = errors.New("the platform's answer is larger than this agent will read")

// ErrUnreadable is returned when the platform answered but the answer is not the JSON the caller expected. It says nothing of what
// the answer held: the caller may be reading a secret.
var ErrUnreadable = errors.New("the platform's answer could not be read")

// GetJSON asks the platform for path with the agent's credential (GET), reads at most maxBytes of the answer and decodes it into out.
// It uses the same routes as every other call (the platform's name, then its fixed fallback addresses, TLS checked against the name),
// never follows a redirect (a redirect could carry the credential to another place), and never puts the answer in an error.
func (c *Client) GetJSON(ctx context.Context, path string, maxBytes int64, out any) error {
	hc := *c.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var lastErr error
	for _, route := range c.order() {
		attempt, cancel := context.WithTimeout(context.WithValue(ctx, routeKey{}, route), 20*time.Second)
		err, answered := c.getOnce(attempt, &hc, path, maxBytes, out)
		cancel()
		if answered {
			c.mu.Lock()
			c.preferred = route
			c.mu.Unlock()
			return err
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	c.mu.Lock()
	c.preferred = 0
	c.mu.Unlock()
	return lastErr
}

// getOnce makes one attempt; answered says the platform replied (so another route would not help).
func (c *Client) getOnce(ctx context.Context, hc *http.Client, path string, maxBytes int64, out any) (error, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err, true
	}
	req.Header.Set("X-Agent-Version", c.Version)
	req.Header.Set("Authorization", "Bearer "+c.Credential)
	res, err := hc.Do(req)
	if err != nil {
		return err, false
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		if res.StatusCode < 400 {
			return &StatusError{Status: res.StatusCode, Message: "the platform redirected the request; this agent does not follow redirects"}, true
		}
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		return &StatusError{Status: res.StatusCode, Message: e.Error}, true
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return err, false
	}
	if int64(len(data)) > maxBytes {
		return ErrTooLarge, true
	}
	if err := json.Unmarshal(data, out); err != nil {
		return ErrUnreadable, true
	}
	return nil, true
}
