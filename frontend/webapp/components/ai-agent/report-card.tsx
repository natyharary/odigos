'use client';

import React from 'react';
import styled from 'styled-components';
import type { AgentReport, AgentRootCause } from '@/types';

const Card = styled.div`
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 12px;
  padding: 14px;
  background: rgba(255, 255, 255, 0.03);
  display: flex;
  flex-direction: column;
  gap: 10px;
`;

const Row = styled.div`
  display: flex;
  align-items: center;
  gap: 10px;
`;

const Badge = styled.span<{ $variant: AgentRootCause }>`
  padding: 4px 10px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  background: ${({ $variant }) =>
    $variant === 'destination_misconfigured'
      ? 'rgba(255, 159, 64, 0.18)'
      : $variant === 'source_not_instrumented'
        ? 'rgba(123, 97, 255, 0.18)'
        : $variant === 'collector_misconfigured'
          ? 'rgba(64, 200, 224, 0.18)'
          : 'rgba(255, 255, 255, 0.12)'};
  color: ${({ $variant }) =>
    $variant === 'destination_misconfigured'
      ? '#ffb070'
      : $variant === 'source_not_instrumented'
        ? '#b8a4ff'
        : $variant === 'collector_misconfigured'
          ? '#8bd7e4'
          : '#dcdcdc'};
`;

const SectionTitle = styled.div`
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: rgba(255, 255, 255, 0.55);
  margin-bottom: 4px;
`;

const List = styled.ul`
  margin: 0;
  padding-left: 18px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 13px;
  color: rgba(255, 255, 255, 0.85);
`;

const RemediationBlock = styled.div`
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 8px;
  padding: 10px;
  font-size: 12px;
  background: rgba(0, 0, 0, 0.2);
`;

const Code = styled.pre`
  margin: 6px 0 0;
  padding: 8px;
  background: rgba(0, 0, 0, 0.35);
  border-radius: 6px;
  font-size: 12px;
  font-family: 'Menlo', 'Monaco', monospace;
  white-space: pre-wrap;
  word-break: break-word;
`;

const labels: Record<AgentRootCause, string> = {
  destination_misconfigured: 'Destination',
  source_not_instrumented: 'Source not instrumented',
  collector_misconfigured: 'Collector',
  unknown: 'Unknown',
};

interface Props {
  report: AgentReport;
}

const ReportCard: React.FC<Props> = ({ report }) => {
  const pct = Math.round((report.confidence || 0) * 100);
  return (
    <Card>
      <Row>
        <Badge $variant={report.root_cause}>{labels[report.root_cause] ?? report.root_cause}</Badge>
        <span style={{ fontSize: 12, color: 'rgba(255,255,255,0.6)' }}>confidence {pct}%</span>
      </Row>

      {report.evidence?.length > 0 && (
        <div>
          <SectionTitle>Evidence</SectionTitle>
          <List>
            {report.evidence.map((item, idx) => (
              <li key={idx}>{item}</li>
            ))}
          </List>
        </div>
      )}

      {report.suggested_actions?.length > 0 && (
        <div>
          <SectionTitle>Suggested actions</SectionTitle>
          <List>
            {report.suggested_actions.map((item, idx) => (
              <li key={idx}>{item}</li>
            ))}
          </List>
        </div>
      )}

      {report.proposed_remediation && (
        <RemediationBlock>
          <SectionTitle>Remediation: {report.proposed_remediation.op}</SectionTitle>
          <div>Status: <strong>{report.proposed_remediation.status}</strong></div>
          {report.proposed_remediation.result && (
            <div style={{ marginTop: 4 }}>Result: {report.proposed_remediation.result}</div>
          )}
          {report.proposed_remediation.rollback_command && (
            <>
              <SectionTitle style={{ marginTop: 8 }}>Rollback</SectionTitle>
              <Code>{report.proposed_remediation.rollback_command}</Code>
            </>
          )}
        </RemediationBlock>
      )}
    </Card>
  );
};

export { ReportCard };
