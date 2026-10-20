// Package taskverify decides whether a task from the platform may be run.
//
// The platform signs the exact bytes of a task's payload with an Ed25519 key and sends the bytes with the signature, so
// this package verifies what it received and parses the task out of those same bytes; it never rebuilds any JSON. The
// rules are the platform's own `verifyTaskEnvelope` (nuit-cloud-servers/src/task-signing.ts), which is this package's test
// oracle: testdata/vectors.json holds tasks signed by that code.
package taskverify

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SigningDomain is prefixed to the payload bytes before signing, so a signature made for a task can never be mistaken for
// another use.
const SigningDomain = "nuit-cloud-task-v1\n"

// Envelope is what the platform hands the agent for one task. Only what verifies may be trusted.
type Envelope struct {
	KeyID     string `json:"keyId"`
	Payload   string `json:"payload"`   // base64url of the exact signed bytes
	Signature string `json:"signature"` // base64url Ed25519 signature over SigningDomain + payload bytes
}

// Key is a public key the agent was given at enrollment.
type Key struct {
	KeyID     string `json:"keyId"`
	PublicKey string `json:"publicKey"` // base64url of the raw 32-byte key
}

// Task is the signed content of an envelope.
type Task struct {
	V              int            `json:"v"`
	ID             string         `json:"id"`
	MachineID      string         `json:"machineId"`
	Name           string         `json:"name"`
	Params         map[string]any `json:"params"`
	IssuedAt       time.Time      `json:"issuedAt"`
	ExpiresAt      time.Time      `json:"expiresAt"`
	TimeoutSeconds int            `json:"timeoutSeconds"`
}

// Context is what the agent knows when it checks a task.
type Context struct {
	MachineID string
	Now       time.Time
	// AlreadySeen reports whether a task id has been run before (replay).
	AlreadySeen func(id string) bool
	// KnownTasks is the agent's own catalog; a task outside it is refused.
	KnownTasks []string
}

// Refusal says why a task was not accepted. Its text is safe to report back to the platform. Task is set only when the
// signature verified, so a task the platform really signed but the agent must not run (expired, unknown to this agent's
// catalog, already run) can be reported on; for anything else it is nil and nothing in it may be trusted.
type Refusal struct {
	Reason string
	Task   *Task
}

func (r *Refusal) Error() string { return r.Reason }

func refuse(format string, a ...any) error { return &Refusal{Reason: fmt.Sprintf(format, a...)} }

// Verify returns the task if, and only if: the key is one the agent trusts, the signature verifies over the bytes received,
// the task is for this machine, it has not expired, it is in the agent's catalog, and it has not been run before.
func Verify(env Envelope, trusted []Key, ctx Context) (*Task, error) {
	var key *Key
	for i := range trusted {
		if trusted[i].KeyID == env.KeyID {
			key = &trusted[i]
			break
		}
	}
	if key == nil {
		return nil, refuse("signed by a key this machine does not trust")
	}
	raw, err := base64.RawURLEncoding.DecodeString(key.PublicKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, refuse("the trusted key is malformed")
	}
	payload, err := base64.RawURLEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, refuse("the payload is not base64url")
	}
	signature, err := base64.RawURLEncoding.DecodeString(env.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, refuse("the signature does not verify")
	}
	signed := append([]byte(SigningDomain), payload...)
	if !ed25519.Verify(ed25519.PublicKey(raw), signed, signature) {
		return nil, refuse("the signature does not verify")
	}

	var task Task
	if err := json.Unmarshal(payload, &task); err != nil {
		return nil, refuse("the payload is not JSON")
	}
	if task.V != 1 {
		return nil, refuse("an unknown payload version")
	}
	// From here the task is authentic, so a refusal can say which task it was about.
	refuseTask := func(reason string) error { return &Refusal{Reason: reason, Task: &task} }
	if task.MachineID != ctx.MachineID {
		return nil, &Refusal{Reason: "the task is for another machine"} // not ours to report on
	}
	if !task.ExpiresAt.After(ctx.Now) {
		return nil, refuseTask("the task has expired")
	}
	if ctx.KnownTasks != nil && !contains(ctx.KnownTasks, task.Name) {
		return nil, refuseTask("the task is not in this agent's catalog")
	}
	if ctx.AlreadySeen != nil && ctx.AlreadySeen(task.ID) {
		return nil, refuseTask("the task was already run")
	}
	if task.Params == nil {
		task.Params = map[string]any{}
	}
	return &task, nil
}

// IsRefusal reports whether err is a Refusal (a task that must not be run), as opposed to an internal error.
func IsRefusal(err error) bool {
	var r *Refusal
	return errors.As(err, &r)
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
