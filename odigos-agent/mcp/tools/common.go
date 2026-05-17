// Package tools wires the cluster-state MCP tools onto the mcp-go server.
//
// Tools are grouped by domain (source, collector, destination, citation) and
// each domain has its own file. Shared plumbing - kube clients, approval
// cache, JSON/error helpers - lives here.
package tools

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	odigosclient "github.com/odigos-io/odigos/api/generated/odigos/clientset/versioned"
	"github.com/odigos-io/odigos/k8sutils/pkg/client"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const defaultApprovalTTL = 5 * time.Minute

// Clients bundles every kube client the MCP tools need.
type Clients struct {
	Core   kubernetes.Interface
	Odigos odigosclient.Interface
	Config *rest.Config
}

// BuildClients dials the cluster, preferring in-cluster credentials and
// falling back to a local kubeconfig for dev. Returns ready-to-use typed
// clients for core/apps and odigos CRDs.
func BuildClients() (*Clients, error) {
	config, err := buildRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("build kube config: %w", err)
	}
	core, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("build core client: %w", err)
	}
	odigos, err := odigosclient.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("build odigos client: %w", err)
	}
	return &Clients{Core: core, Odigos: odigos, Config: config}, nil
}

func buildRESTConfig() (*rest.Config, error) {
	return client.GetClientConfigWithContext(env.GetDefaultKubeConfigPath(), "")
}

// OdigosNamespace returns the namespace odigos system components run in.
func OdigosNamespace() string { return env.GetCurrentNamespace() }

// PendingMutation captures the dry-run state of a proposed mutation, awaiting
// user approval via apply_*. Fields are deliberately concrete - the agent
// rebuilds the runtime object from these on apply rather than trusting cached
// bytes.
//
// create_source populates YAML (greenfield); the rule-patch ops populate
// YAMLBefore + YAMLAfter. TargetDistro is set by the rule-patch ops so apply
// can re-run the same op-specific picker against fresh live state.
type PendingMutation struct {
	Operation    string
	Namespace    string
	WorkloadKind string
	WorkloadName string
	YAML         string
	YAMLBefore   string
	YAMLAfter    string
	Diff         string
	RollbackHint string
	TargetDistro string
	CreatedAt    time.Time
}

// ApprovalCache stores proposed mutations keyed by an opaque request_id. Pure
// in-memory; v1's MCP runs as a single replica so we don't need cross-process
// state. Entries past the TTL are dropped on the next Put/Take.
type ApprovalCache struct {
	mutex   sync.Mutex
	entries map[string]*PendingMutation
	ttl     time.Duration
	now     func() time.Time
}

// NewApprovalCache returns a cache with the given TTL. Pass 0 for the default
// (5 minutes).
func NewApprovalCache(ttl time.Duration) *ApprovalCache {
	if ttl <= 0 {
		ttl = defaultApprovalTTL
	}
	return &ApprovalCache{
		entries: map[string]*PendingMutation{},
		ttl:     ttl,
		now:     time.Now,
	}
}

// Put stores the mutation under a fresh UUID v4 request_id and returns the id.
func (c *ApprovalCache) Put(mutation *PendingMutation) string {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.gcLocked()
	id := uuid.NewString()
	mutation.CreatedAt = c.now()
	c.entries[id] = mutation
	return id
}

// Take pops the mutation for the given request_id. Returns nil if the id is
// unknown or its entry has expired.
func (c *ApprovalCache) Take(id string) *PendingMutation {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.gcLocked()
	mutation, ok := c.entries[id]
	if !ok {
		return nil
	}
	delete(c.entries, id)
	return mutation
}

// Size reports the current live entry count. Test-only.
func (c *ApprovalCache) Size() int {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.gcLocked()
	return len(c.entries)
}

func (c *ApprovalCache) gcLocked() {
	cutoff := c.now().Add(-c.ttl)
	for id, mutation := range c.entries {
		if mutation.CreatedAt.Before(cutoff) {
			delete(c.entries, id)
		}
	}
}

// WriteJSON wraps a value as a structured-only MCP tool result.
func WriteJSON(value any) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultStructuredOnly(value), nil
}

// RequireWorkloadArgs pulls the canonical (namespace, kind, name) string trio
// every workload-targeted MCP tool needs, returning a populated tool-error
// result if any is missing. Callers should `return errResult, nil` when ok is
// false.
func RequireWorkloadArgs(request mcp.CallToolRequest) (namespace, kind, name string, errResult *mcp.CallToolResult, ok bool) {
	namespace, err := request.RequireString("namespace")
	if err != nil {
		result, _ := ToolError("namespace required: %v", err)
		return "", "", "", result, false
	}
	kind, err = request.RequireString("kind")
	if err != nil {
		result, _ := ToolError("kind required: %v", err)
		return "", "", "", result, false
	}
	name, err = request.RequireString("name")
	if err != nil {
		result, _ := ToolError("name required: %v", err)
		return "", "", "", result, false
	}
	return namespace, kind, name, nil, true
}

// ToolError returns an MCP tool result flagged as an error. Callers should
// return (result, nil) - the protocol surfaces IsError to the LLM rather than
// transporting a Go error.
func ToolError(format string, args ...any) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(fmt.Sprintf(format, args...)), nil
}

// TailSlice returns up to the last n elements of xs. n<=0 returns xs unchanged.
func TailSlice[T any](xs []T, n int) []T {
	if n <= 0 || len(xs) <= n {
		return xs
	}
	return xs[len(xs)-n:]
}

// ClampInt clamps v into [low, high]. Convenience for tool-arg bounds.
func ClampInt(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

// UnifiedDiffLines produces a tiny unified-style diff used in approval
// previews. Lines removed from `before` get a `-` prefix, lines added in
// `after` get `+`, lines that match in order pass through with two spaces.
// Not a full LCS diff - it's enough for the UI to render.
func UnifiedDiffLines(before, after string) string {
	var beforeLines, afterLines []string
	if before != "" {
		beforeLines = strings.Split(strings.TrimRight(before, "\n"), "\n")
	}
	if after != "" {
		afterLines = strings.Split(strings.TrimRight(after, "\n"), "\n")
	}
	maxLen := len(beforeLines)
	if len(afterLines) > maxLen {
		maxLen = len(afterLines)
	}
	var builder strings.Builder
	for index := 0; index < maxLen; index++ {
		switch {
		case index < len(beforeLines) && index < len(afterLines) && beforeLines[index] == afterLines[index]:
			builder.WriteString("  ")
			builder.WriteString(beforeLines[index])
			builder.WriteByte('\n')
		default:
			if index < len(beforeLines) {
				builder.WriteString("- ")
				builder.WriteString(beforeLines[index])
				builder.WriteByte('\n')
			}
			if index < len(afterLines) {
				builder.WriteString("+ ")
				builder.WriteString(afterLines[index])
				builder.WriteByte('\n')
			}
		}
	}
	return builder.String()
}
