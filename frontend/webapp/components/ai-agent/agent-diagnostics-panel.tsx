'use client';

import React, { useEffect, useMemo, useState } from 'react';
import styled from 'styled-components';
import { useAIDebug } from '@/hooks';
import type { AgentEvent } from '@/types';
import { useAIAgentPanelStore } from './store';
import { ApprovalModal } from './approval-modal';
import { ReportCard } from './report-card';

const Panel = styled.aside`
  position: fixed;
  top: 60px;
  right: 0;
  bottom: 0;
  width: min(480px, 92vw);
  z-index: 900;
  display: flex;
  flex-direction: column;
  background: ${({ theme }) => theme?.colors?.primary || '#0f1115'};
  border-left: 1px solid rgba(255, 255, 255, 0.08);
  box-shadow: -12px 0 32px rgba(0, 0, 0, 0.4);
  color: ${({ theme }) => theme?.text?.primary || '#fff'};
`;

const Header = styled.div`
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  padding: 14px 16px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.06);
`;

const HeaderText = styled.div`
  display: flex;
  flex-direction: column;
  gap: 2px;
`;

const HeaderTitle = styled.h2`
  margin: 0;
  font-size: 14px;
  font-weight: 600;
`;

const HeaderSubtitle = styled.div`
  font-size: 11px;
  color: rgba(255, 255, 255, 0.55);
  font-family: 'Menlo', 'Monaco', monospace;
`;

const CloseButton = styled.button`
  background: transparent;
  border: 1px solid rgba(255, 255, 255, 0.1);
  border-radius: 6px;
  color: rgba(255, 255, 255, 0.75);
  width: 28px;
  height: 28px;
  cursor: pointer;
  font-size: 16px;
  line-height: 1;
`;

const Body = styled.div`
  flex: 1;
  overflow-y: auto;
  padding: 12px 16px 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
`;

const StatusLine = styled.div`
  font-size: 12px;
  color: rgba(255, 255, 255, 0.7);
`;

const StatusPill = styled.span<{ $status: string }>`
  display: inline-block;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  margin-right: 6px;
  background: ${({ $status }) => {
    if ($status === 'streaming') return 'rgba(64, 200, 224, 0.18)';
    if ($status === 'awaiting_approval') return 'rgba(255, 200, 90, 0.18)';
    if ($status === 'done') return 'rgba(80, 220, 120, 0.18)';
    if ($status === 'error') return 'rgba(220, 90, 90, 0.18)';
    if ($status === 'cancelled') return 'rgba(255, 255, 255, 0.1)';
    return 'rgba(255, 255, 255, 0.08)';
  }};
  color: ${({ $status }) => {
    if ($status === 'streaming') return '#8bd7e4';
    if ($status === 'awaiting_approval') return '#ffd17a';
    if ($status === 'done') return '#7be09e';
    if ($status === 'error') return '#f0a0a0';
    return 'rgba(255, 255, 255, 0.7)';
  }};
`;

const ErrorBox = styled.div`
  border: 1px solid rgba(220, 90, 90, 0.4);
  background: rgba(220, 90, 90, 0.08);
  color: #f0a0a0;
  padding: 8px 10px;
  border-radius: 6px;
  font-size: 12px;
`;

const StepList = styled.div`
  display: flex;
  flex-direction: column;
  gap: 6px;
`;

const StepRow = styled.div`
  display: flex;
  gap: 8px;
  align-items: flex-start;
  font-size: 12px;
  line-height: 1.45;
  color: rgba(255, 255, 255, 0.82);
`;

const PhaseChip = styled.span<{ $phase: string }>`
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 1px 6px;
  border-radius: 4px;
  flex-shrink: 0;
  margin-top: 2px;
  background: ${({ $phase }) =>
    $phase === 'source'
      ? 'rgba(123, 97, 255, 0.18)'
      : $phase === 'collector'
        ? 'rgba(64, 200, 224, 0.18)'
        : $phase === 'destination'
          ? 'rgba(255, 159, 64, 0.18)'
          : $phase === 'triage'
            ? 'rgba(255, 255, 255, 0.1)'
            : $phase === 'synthesize'
              ? 'rgba(80, 220, 120, 0.18)'
              : 'rgba(255, 255, 255, 0.06)'};
  color: ${({ $phase }) =>
    $phase === 'source'
      ? '#b8a4ff'
      : $phase === 'collector'
        ? '#8bd7e4'
        : $phase === 'destination'
          ? '#ffb070'
          : $phase === 'synthesize'
            ? '#7be09e'
            : 'rgba(255, 255, 255, 0.75)'};
`;

const StepKind = styled.span<{ $kind: string }>`
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 1px 6px;
  border-radius: 4px;
  flex-shrink: 0;
  margin-top: 2px;
  background: rgba(255, 255, 255, 0.05);
  color: rgba(255, 255, 255, 0.6);
`;

const StepBody = styled.span`
  word-break: break-word;
`;

const Toggle = styled.button`
  align-self: flex-start;
  background: transparent;
  border: 0;
  color: rgba(255, 255, 255, 0.55);
  font-size: 12px;
  cursor: pointer;
  padding: 4px 0;
`;

function renderStep(event: AgentEvent, index: number): React.ReactNode {
  if (event.event === 'session' || event.event === 'done' || event.event === 'approval_required' ||
    event.event === 'approval_resolved' || event.event === 'report' || event.event === 'error') {
    return null;
  }
  if (event.event === 'step') {
    return (
      <StepRow key={index}>
        <PhaseChip $phase={event.data.phase}>{event.data.phase}</PhaseChip>
        <StepBody>{event.data.message}</StepBody>
      </StepRow>
    );
  }
  if (event.event === 'finding') {
    return (
      <StepRow key={index}>
        <PhaseChip $phase={event.data.phase}>{event.data.phase}</PhaseChip>
        <StepKind $kind="finding">finding</StepKind>
        <StepBody>{event.data.summary}</StepBody>
      </StepRow>
    );
  }
  if (event.event === 'knowledge_query') {
    return (
      <StepRow key={index}>
        <PhaseChip $phase={event.data.phase}>{event.data.phase}</PhaseChip>
        <StepKind $kind="graph">graph</StepKind>
        <StepBody>
          {event.data.tool}
          {event.data.query ? ` · ${event.data.query}` : ''}
        </StepBody>
      </StepRow>
    );
  }
  if (event.event === 'codebase_read') {
    return (
      <StepRow key={index}>
        <PhaseChip $phase={event.data.phase || 'codebase'}>{event.data.phase || 'codebase'}</PhaseChip>
        <StepKind $kind="code">code</StepKind>
        <StepBody>
          {event.data.path}
          {event.data.lines ? `:${event.data.lines}` : ''}
        </StepBody>
      </StepRow>
    );
  }
  return null;
}

const AgentDiagnosticsPanel: React.FC = () => {
  const { isOpen, target, close } = useAIAgentPanelStore();
  const { state, start, cancel, reset, approve } = useAIDebug();
  const [showAllSteps, setShowAllSteps] = useState(false);

  // Start the stream when the panel opens with a target; reset when the user
  // closes the panel so a re-open starts clean.
  useEffect(() => {
    if (isOpen && target && state.status === 'idle') {
      start({ namespace: target.namespace, kind: String(target.kind), name: target.name });
    }
  }, [isOpen, target, state.status, start]);

  const handleClose = () => {
    cancel();
    reset();
    close();
  };

  const visibleSteps = useMemo(() => state.steps.filter((event) => renderStep(event, 0) !== null), [state.steps]);
  const latestStep = useMemo(() => {
    for (let i = state.steps.length - 1; i >= 0; i--) {
      const event = state.steps[i];
      if (event.event === 'step') return event.data.message;
      if (event.event === 'finding') return event.data.summary;
    }
    return null;
  }, [state.steps]);

  if (!isOpen || !target) return null;

  const showStepLog = !state.report || showAllSteps;

  return (
    <>
      <Panel>
        <Header>
          <HeaderText>
            <HeaderTitle>Fix with AI</HeaderTitle>
            <HeaderSubtitle>
              {String(target.kind)} · {target.namespace}/{target.name}
            </HeaderSubtitle>
          </HeaderText>
          <CloseButton type="button" onClick={handleClose} title="Close">
            ×
          </CloseButton>
        </Header>

        <Body>
          <StatusLine>
            <StatusPill $status={state.status}>{state.status.replace('_', ' ')}</StatusPill>
            {latestStep || (state.status === 'idle' ? 'starting…' : '')}
          </StatusLine>

          {state.error && <ErrorBox>{state.error}</ErrorBox>}

          {state.report && <ReportCard report={state.report} />}

          {state.report && (
            <Toggle type="button" onClick={() => setShowAllSteps((v) => !v)}>
              {showAllSteps ? 'Hide reasoning' : `Show reasoning (${visibleSteps.length} steps)`}
            </Toggle>
          )}

          {showStepLog && (
            <StepList>
              {visibleSteps.map((event, idx) => renderStep(event, idx))}
            </StepList>
          )}
        </Body>
      </Panel>

      {state.pendingApproval && (
        <ApprovalModal
          approval={state.pendingApproval.data}
          receivedAt={state.pendingApproval.receivedAt}
          onDecide={approve}
        />
      )}
    </>
  );
};

export { AgentDiagnosticsPanel };
