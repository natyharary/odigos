'use client';

import React, { useEffect, useMemo, useState } from 'react';
import styled from 'styled-components';
import type { ApprovalDecision, ApprovalRequiredData } from '@/types';

const APPROVAL_TIMEOUT_SECONDS = 300;

const Backdrop = styled.div`
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.55);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1100;
`;

const Modal = styled.div`
  width: min(640px, 92vw);
  max-height: 86vh;
  background: ${({ theme }) => theme?.colors?.primary || '#16181d'};
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 12px;
  padding: 18px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  color: ${({ theme }) => theme?.text?.primary || '#fff'};
  overflow: hidden;
`;

const Title = styled.h3`
  margin: 0;
  font-size: 16px;
  font-weight: 600;
`;

const Subtitle = styled.div`
  font-size: 12px;
  color: rgba(255, 255, 255, 0.6);
`;

const SectionTitle = styled.div`
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: rgba(255, 255, 255, 0.55);
`;

const Pre = styled.pre`
  margin: 0;
  padding: 10px;
  background: rgba(0, 0, 0, 0.35);
  border: 1px solid rgba(255, 255, 255, 0.06);
  border-radius: 8px;
  font-size: 12px;
  font-family: 'Menlo', 'Monaco', monospace;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 240px;
  overflow: auto;
`;

const Diff = styled(Pre)`
  background: rgba(20, 60, 30, 0.25);
`;

const Callout = styled.div`
  padding: 10px 12px;
  border-radius: 8px;
  background: rgba(80, 140, 220, 0.12);
  border: 1px solid rgba(80, 140, 220, 0.35);
  color: rgba(220, 235, 255, 0.9);
  font-size: 12px;
  line-height: 1.45;
`;

const CopyBox = styled.div`
  display: flex;
  align-items: stretch;
  gap: 6px;
`;

const CopyButton = styled.button`
  border: 1px solid rgba(255, 255, 255, 0.12);
  background: rgba(255, 255, 255, 0.04);
  color: rgba(255, 255, 255, 0.85);
  border-radius: 6px;
  padding: 0 10px;
  cursor: pointer;
  font-size: 12px;
`;

const ButtonRow = styled.div`
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-top: 4px;
`;

const Action = styled.button<{ $variant: 'approve' | 'deny' }>`
  padding: 8px 16px;
  border-radius: 6px;
  border: 1px solid
    ${({ $variant }) => ($variant === 'approve' ? 'rgba(80, 220, 120, 0.6)' : 'rgba(220, 90, 90, 0.6)')};
  background: ${({ $variant }) =>
    $variant === 'approve' ? 'rgba(80, 220, 120, 0.15)' : 'rgba(220, 90, 90, 0.12)'};
  color: ${({ $variant }) => ($variant === 'approve' ? '#7be09e' : '#f0a0a0')};
  font-weight: 600;
  cursor: pointer;

  &:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
`;

interface Props {
  approval: ApprovalRequiredData;
  receivedAt: number;
  onDecide: (decision: ApprovalDecision) => Promise<void> | void;
}

function formatCountdown(seconds: number): string {
  if (seconds <= 0) return 'expired';
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return `${m}:${s.toString().padStart(2, '0')}`;
}

const ApprovalModal: React.FC<Props> = ({ approval, receivedAt, onDecide }) => {
  const [submitting, setSubmitting] = useState<ApprovalDecision | null>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const interval = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(interval);
  }, []);

  const remaining = useMemo(() => {
    const elapsed = Math.floor((now - receivedAt) / 1000);
    return Math.max(0, APPROVAL_TIMEOUT_SECONDS - elapsed);
  }, [now, receivedAt]);

  const expired = remaining <= 0;

  const handle = async (decision: ApprovalDecision) => {
    if (submitting || expired) return;
    setSubmitting(decision);
    setSubmitError(null);
    try {
      await onDecide(decision);
    } catch (error) {
      setSubmitError((error as Error)?.message || 'failed to send decision');
    } finally {
      setSubmitting(null);
    }
  };

  const copy = (text: string) => {
    if (typeof navigator !== 'undefined' && navigator.clipboard) {
      navigator.clipboard.writeText(text).catch(() => undefined);
    }
  };

  const callout = buildCallout(approval);
  const showBeforeAfter = approval.op !== 'create_source' && (approval.yaml_before || approval.yaml_after);

  return (
    <Backdrop>
      <Modal>
        <div>
          <Title>Approval required · {approval.op}</Title>
          <Subtitle>The agent wants to apply a change. Review below before continuing.</Subtitle>
        </div>

        {callout && <Callout>{callout}</Callout>}

        {showBeforeAfter ? (
          <>
            <div>
              <SectionTitle>Before</SectionTitle>
              <Pre>{approval.yaml_before || '(no existing InstrumentationRule)'}</Pre>
            </div>
            <div>
              <SectionTitle>After</SectionTitle>
              <Pre>{approval.yaml_after || '(empty)'}</Pre>
            </div>
          </>
        ) : (
          <div>
            <SectionTitle>YAML</SectionTitle>
            <Pre>{approval.yaml || '(empty)'}</Pre>
          </div>
        )}

        {approval.diff && (
          <div>
            <SectionTitle>Diff</SectionTitle>
            <Diff>{approval.diff}</Diff>
          </div>
        )}

        {approval.rollback_command && (
          <div>
            <SectionTitle>Rollback</SectionTitle>
            <CopyBox>
              <Pre style={{ flex: 1 }}>{approval.rollback_command}</Pre>
              <CopyButton type="button" onClick={() => copy(approval.rollback_command)}>
                Copy
              </CopyButton>
            </CopyBox>
          </div>
        )}

        {submitError && (
          <Subtitle style={{ color: '#f0a0a0' }}>
            Decision failed: {submitError}
          </Subtitle>
        )}

        <ButtonRow>
          <Subtitle>
            {expired ? 'Approval window expired - the agent will time out' : `Expires in ${formatCountdown(remaining)}`}
          </Subtitle>
          <div style={{ display: 'flex', gap: 8 }}>
            <Action
              $variant="deny"
              type="button"
              onClick={() => handle('deny')}
              disabled={submitting !== null || expired}
            >
              {submitting === 'deny' ? 'Denying…' : 'Deny'}
            </Action>
            <Action
              $variant="approve"
              type="button"
              onClick={() => handle('approve')}
              disabled={submitting !== null || expired}
            >
              {submitting === 'approve' ? 'Approving…' : 'Approve & apply'}
            </Action>
          </div>
        </ButtonRow>
      </Modal>
    </Backdrop>
  );
};

function buildCallout(approval: ApprovalRequiredData): string | null {
  const context = (approval.context || {}) as Record<string, string>;
  switch (approval.op) {
    case 'override_distro': {
      const language = context.language || 'workload';
      const from = context.from_distro || '(unknown)';
      const to = context.to_distro || '(unknown)';
      return `Changing ${language} distro from ${from} → ${to}.`;
    }
    case 'enable_obi': {
      const language = context.language || 'workload';
      const ebpf = context.ebpf_distro_name || '(unknown)';
      return `Will switch the ${language} workload to eBPF-based instrumentation (${ebpf}). Pods may need a restart for the change to take effect.`;
    }
    case 'disable_obi': {
      const removed = context.removed_distro || '(unknown)';
      const restored = context.restored_to || '(unknown)';
      return `Will revert OBI (${removed}) and use the language SDK (${restored}).`;
    }
    default:
      return null;
  }
}

export { ApprovalModal };
