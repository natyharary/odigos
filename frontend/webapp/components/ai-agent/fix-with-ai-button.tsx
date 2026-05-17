'use client';

import React from 'react';
import styled from 'styled-components';
import { useDrawerStore } from '@odigos/ui-kit/store';
import { EntityTypes, type WorkloadId } from '@odigos/ui-kit/types';
import { useAIAgentPanelStore } from './store';

// The Source drawer ships from @odigos/ui-kit and doesn't expose extension
// slots, so we render the "Fix with AI" button as a floating overlay that
// appears whenever a Source drawer is open. We pull the selected source from
// the ui-kit drawer store, not from props.

const Floating = styled.button`
  position: fixed;
  top: 80px;
  right: 520px;
  z-index: 1000;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 14px;
  border: 1px solid ${({ theme }) => theme?.colors?.secondary || '#7B61FF'};
  border-radius: 999px;
  background: ${({ theme }) => theme?.colors?.dropdown_bg || '#202225'};
  color: ${({ theme }) => theme?.text?.primary || '#fff'};
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  box-shadow: 0 6px 20px rgba(0, 0, 0, 0.35);

  &:hover {
    background: ${({ theme }) => theme?.colors?.dropdown_bg_2 || '#2a2c30'};
  }
`;

const Sparkle = () => (
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden>
    <path d="M12 2l1.8 6.2L20 10l-6.2 1.8L12 18l-1.8-6.2L4 10l6.2-1.8L12 2z" fill="currentColor" />
  </svg>
);

const FixWithAIButton: React.FC = () => {
  const { drawerType, drawerEntityId } = useDrawerStore();
  const open = useAIAgentPanelStore((s) => s.open);

  if (drawerType !== EntityTypes.Source || !drawerEntityId) return null;
  const workload = drawerEntityId as WorkloadId;
  if (!workload.namespace || !workload.name || !workload.kind) return null;

  return (
    <Floating onClick={() => open(workload)} type="button" title="Diagnose this source with the AI agent">
      <Sparkle />
      Fix with AI
    </Floating>
  );
};

export { FixWithAIButton };
