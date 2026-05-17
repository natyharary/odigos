// Types mirror the SSE event schema emitted by the odigos-ai-agent FastAPI
// surface in odigos-agent/agent/src/odigos_agent/events.py. Keep in sync.

export type AgentRootCause =
  | 'destination_misconfigured'
  | 'source_not_instrumented'
  | 'collector_misconfigured'
  | 'unknown';

export type RemediationStatus =
  | 'pending_approval'
  | 'approved_applied'
  | 'denied'
  | 'timed_out'
  | 'failed';

export type RemediationOp =
  | 'create_source'
  | 'override_distro'
  | 'enable_obi'
  | 'disable_obi';

export interface ProposedRemediation {
  op: RemediationOp;
  request_id: string;
  yaml: string;
  yaml_before?: string;
  yaml_after?: string;
  diff: string;
  rollback_command: string;
  status: RemediationStatus;
  result: string | null;
  context?: Record<string, unknown>;
}

export interface AgentReport {
  root_cause: AgentRootCause;
  confidence: number;
  evidence: string[];
  suggested_actions: string[];
  proposed_remediation: ProposedRemediation | null;
}

export interface ApprovalRequiredData {
  request_id: string;
  op: RemediationOp;
  yaml: string;
  yaml_before?: string;
  yaml_after?: string;
  diff: string;
  rollback_command: string;
  context?: Record<string, unknown>;
}

export type ApprovalDecision = 'approve' | 'deny' | 'timed_out';

// Phase tags mirror the subgraph names + the framing nodes in
// odigos-agent/agent/src/odigos_agent/graph.py.
export type AgentPhase =
  | 'input'
  | 'triage'
  | 'source'
  | 'collector'
  | 'destination'
  | 'apply_remediation'
  | 'synthesize';

export interface SessionEventData {
  debug_session_id: string;
}

export interface StepEventData {
  phase: AgentPhase;
  message: string;
  ts: number;
  detail?: Record<string, unknown>;
}

export interface KnowledgeQueryEventData {
  phase: AgentPhase;
  tool: string;
  query: string;
}

export interface CodebaseReadEventData {
  phase?: AgentPhase;
  path: string;
  lines: string;
}

export interface FindingEventData {
  phase: AgentPhase;
  summary: string;
}

export interface ApprovalResolvedEventData {
  request_id: string;
  decision: ApprovalDecision;
  result?: string;
}

export interface AgentErrorEventData {
  message: string;
  code?: string;
}

export type AgentEvent =
  | { event: 'session'; data: SessionEventData }
  | { event: 'step'; data: StepEventData }
  | { event: 'knowledge_query'; data: KnowledgeQueryEventData }
  | { event: 'codebase_read'; data: CodebaseReadEventData }
  | { event: 'finding'; data: FindingEventData }
  | { event: 'approval_required'; data: ApprovalRequiredData }
  | { event: 'approval_resolved'; data: ApprovalResolvedEventData }
  | { event: 'report'; data: AgentReport }
  | { event: 'done'; data: Record<string, never> }
  | { event: 'error'; data: AgentErrorEventData };

export interface DebugInput {
  namespace: string;
  kind: string;
  name: string;
}
