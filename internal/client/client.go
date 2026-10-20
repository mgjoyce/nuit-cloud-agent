// Package client is the agent's side of the platform's HTTP API. The agent only ever calls out; nothing here listens.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mgjoyce/nuit-cloud-agent/internal/taskverify"
)

// Client talks to one platform.
type Client struct {
	BaseURL    string
	HTTP       *http.Client
	Version    string
	Credential string
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
	return &Client{BaseURL: u.String(), HTTP: &http.Client{Timeout: 30 * time.Second}, Version: version}, nil
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
}

// CheckInResponse is the answer to a routine call: when to call again, and the signed tasks waiting.
type CheckInResponse struct {
	MachineID           string                `json:"machineId"`
	Name                string                `json:"name"`
	PollIntervalSeconds int                   `json:"pollIntervalSeconds"`
	Tasks               []taskverify.Envelope `json:"tasks"`
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

func (c *Client) do(ctx context.Context, path string, body any, authenticated bool, out any) error {
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
