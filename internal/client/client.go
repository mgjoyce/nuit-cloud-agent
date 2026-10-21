// Package client is the agent's side of the platform's HTTP API. The agent only ever calls out; nothing here listens.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mgjoyce/nuit-cloud-agent/internal/taskverify"
)

// Client talks to one platform.
//
// It reaches it by the platform's name, and, if that does not work, by fixed addresses the platform has told it about (see
// SetFallbacks). A fallback is only a different place to connect to: the request is still for the platform's name, so the
// certificate is checked against that name and an address that is not really the platform cannot be talked to. This is what keeps
// an agent in touch when the platform's name cannot be resolved, or what the name points at is down.
type Client struct {
	BaseURL    string
	HTTP       *http.Client
	Version    string
	Credential string

	mu        sync.Mutex
	fallbacks []string
	// preferred is the route that worked last: 0 is the name, n is fallbacks[n-1].
	preferred int
}

type routeKey struct{}

// MaxFallbacks is how many fixed addresses an agent keeps.
const MaxFallbacks = 4

// validFallback is ValidFallback; a test replaces it to use a loopback server.
var validFallback = ValidFallback

// ValidFallback reports whether s is `host:port` with a private IPv4 host. A platform that tells an agent to connect to a public
// address is refused: fallbacks exist for the platform's own network.
func ValidFallback(s string) bool {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil || !ip.IsPrivate() {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

// SetFallbacks replaces the fixed addresses to try when the name does not work, keeping only valid ones (at most MaxFallbacks),
// and returns what it kept.
func (c *Client) SetFallbacks(list []string) []string {
	var kept []string
	seen := map[string]bool{}
	for _, a := range list {
		if len(kept) < MaxFallbacks && validFallback(a) && !seen[a] {
			kept = append(kept, a)
			seen[a] = true
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fallbacks = kept
	if c.preferred > len(kept) {
		c.preferred = 0
	}
	return kept
}

// Route says how the platform was last reached: its name, or a fixed address.
func (c *Client) Route() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.preferred == 0 || c.preferred > len(c.fallbacks) {
		return "by name"
	}
	return "by address " + c.fallbacks[c.preferred-1]
}

// order lists the routes to try: the one that worked last, then the name, then the rest.
func (c *Client) order() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	routes := []int{c.preferred}
	for r := 0; r <= len(c.fallbacks); r++ {
		if r != c.preferred {
			routes = append(routes, r)
		}
	}
	return routes
}

// dial is the transport's way of connecting: to the address the request names, unless the request was given a fallback route.
func (c *Client) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 10 * time.Second}
	if r, ok := ctx.Value(routeKey{}).(int); ok && r > 0 {
		c.mu.Lock()
		var target string
		if r <= len(c.fallbacks) {
			target = c.fallbacks[r-1]
		}
		c.mu.Unlock()
		if target != "" {
			return d.DialContext(ctx, network, target)
		}
	}
	return d.DialContext(ctx, network, addr)
}

// New makes a client. It refuses a plain-http address unless allowInsecureHTTP is set: the enrollment code and the agent's
// credential must not cross a network in the clear.
func New(baseURL, version string, allowInsecureHTTP bool) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%q is not a platform address", baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && allowInsecureHTTP) {
		return nil, errors.New("the platform address must be https:// (pass --insecure-http only for a trusted test network)")
	}
	c := &Client{BaseURL: u.String(), Version: version}
	// Each request makes its own connection, so which route it used is never a pooled connection's doing.
	c.HTTP = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: c.dial, DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second, Proxy: http.ProxyFromEnvironment}}
	return c, nil
}

// StatusError is a refusal or failure the platform answered with.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("the platform answered %d: %s", e.Status, e.Message)
}

// Unauthorized reports a 401: the code, or the agent's credential, is not (or is no longer) accepted.
func (e *StatusError) Unauthorized() bool { return e.Status == http.StatusUnauthorized }

// EnrollResponse is the platform's answer to a first call.
type EnrollResponse struct {
	MachineID           string `json:"machineId"`
	Name                string `json:"name"`
	Credential          string `json:"credential"`
	PollIntervalSeconds int    `json:"pollIntervalSeconds"`
	TaskSigning         struct {
		Keys []taskverify.Key `json:"keys"`
	} `json:"taskSigning"`
	// FallbackAddresses are fixed places to connect to when the platform's name does not work (omitted: none configured).
	FallbackAddresses []string `json:"fallbackAddresses,omitempty"`
}

// CheckInResponse is the answer to a routine call: when to call again, and the signed tasks waiting.
type CheckInResponse struct {
	MachineID           string                `json:"machineId"`
	Name                string                `json:"name"`
	PollIntervalSeconds int                   `json:"pollIntervalSeconds"`
	Tasks               []taskverify.Envelope `json:"tasks"`
	// FallbackAddresses: see EnrollResponse. Absent means unchanged; an empty list clears them.
	FallbackAddresses *[]string `json:"fallbackAddresses,omitempty"`
}

// Report is what the agent says about a task.
type Report struct {
	Status   string `json:"status"` // running, done or failed
	ExitCode *int   `json:"exitCode,omitempty"`
	Output   string `json:"output,omitempty"`
	Result   any    `json:"result,omitempty"`
}

// Enroll trades an enrollment code for an identity of its own.
func (c *Client) Enroll(ctx context.Context, code, hostname string) (*EnrollResponse, error) {
	var out EnrollResponse
	err := c.do(ctx, "/api/agent/enroll", map[string]string{"code": code, "hostname": hostname, "agentVersion": c.Version}, false, &out)
	return &out, err
}

// CheckIn is the routine call.
func (c *Client) CheckIn(ctx context.Context) (*CheckInResponse, error) {
	var out CheckInResponse
	err := c.do(ctx, "/api/agent/checkin", struct{}{}, true, &out)
	return &out, err
}

// ReportTask tells the platform how a task is going. A 409 (the platform already has a finished result) is not an error: the
// agent has nothing more to do about that task.
func (c *Client) ReportTask(ctx context.Context, taskID string, report Report) error {
	err := c.do(ctx, "/api/agent/tasks/"+url.PathEscape(taskID)+"/result", report, true, nil)
	var se *StatusError
	if errors.As(err, &se) && se.Status == http.StatusConflict {
		return nil
	}
	return err
}

// RotateCredential gets a new credential; the old one stops working at once, so the caller must save the new one first thing.
func (c *Client) RotateCredential(ctx context.Context) (string, error) {
	var out struct {
		Credential string `json:"credential"`
	}
	err := c.do(ctx, "/api/agent/rotate-credential", struct{}{}, true, &out)
	return out.Credential, err
}

// do makes a call. If the platform cannot be reached (not: if it answers with a refusal), it tries the other routes, and remembers the one
// that worked.
func (c *Client) do(ctx context.Context, path string, body any, authenticated bool, out any) error {
	var lastErr error
	for _, route := range c.order() {
		attempt, cancel := context.WithTimeout(context.WithValue(ctx, routeKey{}, route), 20*time.Second)
		err := c.once(attempt, path, body, authenticated, out)
		cancel()
		var se *StatusError
		if err == nil || errors.As(err, &se) {
			// the platform answered (even if with a refusal): this route works
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
	c.preferred = 0 // next time start with the name again
	c.mu.Unlock()
	return lastErr
}

func (c *Client) once(ctx context.Context, path string, body any, authenticated bool, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Version", c.Version)
	if authenticated {
		req.Header.Set("Authorization", "Bearer "+c.Credential)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		return &StatusError{Status: res.StatusCode, Message: e.Error}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
