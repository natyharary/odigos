import { create } from 'zustand';
import type { WorkloadId } from '@odigos/ui-kit/types';

interface AIAgentPanelState {
  isOpen: boolean;
  target: WorkloadId | null;
  open: (target: WorkloadId) => void;
  close: () => void;
}

export const useAIAgentPanelStore = create<AIAgentPanelState>((set) => ({
  isOpen: false,
  target: null,
  open: (target) => set({ isOpen: true, target }),
  close: () => set({ isOpen: false, target: null }),
}));
