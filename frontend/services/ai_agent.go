package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

// SSE proxy for the in-cluster odigos-ai-agent. The agent runs as a separate
// pod/Service and never receives direct browser traffic - the UI calls these
// endpoints, the Go backend forwards to the agent with the bearer token, and
// streams events back unmodified.

const (
	defaultAIAgentURL = "http://odigos-ai-agent.odigos-system:8765"

	aiAgentURLEnvVar   = "AI_AGENT_URL"
	aiAgentTokenEnvVar = "AI_AGENT_TOKEN"
)

// SSE streams are long-lived; no client-side timeout. Per-request context
// (carrying client-disconnect) handles cancellation upstream.
var aiAgentHTTPClient = &http.Client{Timeout: 0}

var aiAgentTokenWarnOnce sync.Once

type aiDebugRequest struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
}

type aiApproveRequest struct {
	Decision string `json:"decision"`
}

func aiAgentBaseURL() string {
	return strings.TrimRight(env.GetEnvVarOrDefault(aiAgentURLEnvVar, defaultAIAgentURL), "/")
}

func aiAgentToken() string {
	return os.Getenv(aiAgentTokenEnvVar)
}

func setAIAgentAuth(req *http.Request) {
	token := aiAgentToken()
	if token == "" {
		aiAgentTokenWarnOnce.Do(func() {
			fmt.Fprintln(os.Stderr, "warning: AI_AGENT_TOKEN not set; forwarding /api/ai/* requests without Authorization header")
		})
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
}

// AIAgentDebug proxies POST /api/ai/debug to the agent's POST /debug and
// streams the SSE response back to the browser.
func AIAgentDebug(c *gin.Context) {
	var body aiDebugRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "detail": err.Error()})
		return
	}
	if body.Namespace == "" || body.Kind == "" || body.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "namespace, kind, and name are required"})
		return
	}

	upstreamBody, err := json.Marshal(body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encode upstream body"})
		return
	}

	upstreamURL := aiAgentBaseURL() + "/debug"
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build upstream request", "detail": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	setAIAgentAuth(req)

	resp, err := aiAgentHTTPClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to reach AI agent", "detail": err.Error()})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		relayUpstreamError(c, resp)
		return
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming unsupported"})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	// Disables buffering in proxies like nginx so events surface immediately.
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flusher.Flush()

	streamWithFlush(c.Writer, flusher, resp.Body)
}

// AIAgentApprove proxies POST /api/ai/approve/:session/:request_id to the
// agent's POST /approve/{sid}/{rid}. The agent returns 204 on success; we
// relay status + body.
func AIAgentApprove(c *gin.Context) {
	session := c.Param("session")
	requestID := c.Param("request_id")
	if session == "" || requestID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session and request_id are required"})
		return
	}

	var body aiApproveRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "detail": err.Error()})
		return
	}
	if body.Decision != "approve" && body.Decision != "deny" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "decision must be 'approve' or 'deny'"})
		return
	}

	upstreamBody, err := json.Marshal(body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encode upstream body"})
		return
	}

	upstreamURL := fmt.Sprintf("%s/approve/%s/%s",
		aiAgentBaseURL(),
		url.PathEscape(session),
		url.PathEscape(requestID),
	)
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build upstream request", "detail": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	setAIAgentAuth(req)

	resp, err := aiAgentHTTPClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to reach AI agent", "detail": err.Error()})
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if len(respBody) == 0 {
		c.Status(resp.StatusCode)
		return
	}
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), respBody)
}

func relayUpstreamError(c *gin.Context, resp *http.Response) {
	body, _ := io.ReadAll(resp.Body)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	if len(body) == 0 {
		c.JSON(resp.StatusCode, gin.H{"error": fmt.Sprintf("AI agent returned %d", resp.StatusCode)})
		return
	}
	c.Data(resp.StatusCode, contentType, body)
}

// streamWithFlush copies the agent's SSE bytes through to the gin
// writer, flushing on each read so each event reaches the browser as
// soon as it arrives upstream.
func streamWithFlush(w io.Writer, flusher http.Flusher, src io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			flusher.Flush()
		}
		if err != nil {
			return
		}
	}
}
