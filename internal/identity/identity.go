// Package identity defines the opaque run and node-visit identifiers used
// across relay-flow. It imports no other relay-flow package.
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"strings"
)

// RunID identifies one durable run. Opaque to consumers.
type RunID string

// NodeVisitID identifies one entry into a workflow node. Opaque to consumers.
type NodeVisitID string

// NewRunID returns the deterministic run ID for a (repo, workflow, ticket)
// triple. Each component is path-escaped so the joined ID is delimiter-safe.
func NewRunID(repo, workflow, ticket string) RunID {
	return RunID(strings.Join([]string{
		url.PathEscape(repo),
		url.PathEscape(workflow),
		url.PathEscape(ticket),
	}, "/"))
}

// NewNodeVisitID returns a fresh random node-visit ID. Generation happens
// once per node entry as a durable replay-safe side effect; this function
// only produces the random value.
func NewNodeVisitID() NodeVisitID {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return NodeVisitID(hex.EncodeToString(b[:]))
}
