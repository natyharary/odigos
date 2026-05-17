import { useCallback, useEffect, useReducer, useRef } from 'react';
import { postApproval, streamAIDebug, type StreamHandle, type StreamCloseReason } from '@/lib/aiAgent';
import type {
  AgentEvent,
  AgentReport,
  ApprovalDecision,
  ApprovalRequiredData,
  DebugInput,
} from '@/types';

export type DebugStatus = 'idle' | 'streaming' | 'awaiting_approval' | 'done' | 'error' | 'cancelled';

export interface PendingApproval {
  data: ApprovalRequiredData;
  receivedAt: number;
}

export interface DebugState {
  status: DebugStatus;
  sessionId: string | null;
  steps: AgentEvent[];
  pendingApproval: PendingApproval | null;
  report: AgentReport | null;
  error: string | null;
}

const initialState: DebugState = {
  status: 'idle',
  sessionId: null,
  steps: [],
  pendingApproval: null,
  report: null,
  error: null,
};

type Action =
  | { type: 'start' }
  | { type: 'event'; event: AgentEvent }
  | { type: 'close'; reason: StreamCloseReason; error?: string }
  | { type: 'reset' };

function reducer(state: DebugState, action: Action): DebugState {
  switch (action.type) {
    case 'start':
      return { ...initialState, status: 'streaming' };
    case 'reset':
      return initialState;
    case 'event': {
      const { event } = action;
      const stepsAppend = [...state.steps, event];
      switch (event.event) {
        case 'session':
          return { ...state, sessionId: event.data.debug_session_id, steps: stepsAppend };
        case 'approval_required':
          return {
            ...state,
            status: 'awaiting_approval',
            pendingApproval: { data: event.data, receivedAt: Date.now() },
            steps: stepsAppend,
          };
        case 'approval_resolved':
          return {
            ...state,
            status: 'streaming',
            pendingApproval: null,
            steps: stepsAppend,
          };
        case 'report':
          return { ...state, report: event.data, steps: stepsAppend };
        case 'error':
          return {
            ...state,
            status: 'error',
            error: event.data.message,
            steps: stepsAppend,
          };
        default:
          return { ...state, steps: stepsAppend };
      }
    }
    case 'close': {
      if (action.reason === 'aborted') return { ...state, status: 'cancelled' };
      if (action.reason === 'error') {
        return { ...state, status: 'error', error: action.error || 'stream error' };
      }
      // done
      if (state.status === 'error') return state;
      return { ...state, status: 'done' };
    }
    default:
      return state;
  }
}

export interface UseAIDebug {
  state: DebugState;
  start: (input: DebugInput) => void;
  cancel: () => void;
  reset: () => void;
  approve: (decision: ApprovalDecision) => Promise<void>;
}

export function useAIDebug(): UseAIDebug {
  const [state, dispatch] = useReducer(reducer, initialState);
  const streamRef = useRef<StreamHandle | null>(null);

  // Cancel any in-flight stream on unmount.
  useEffect(() => {
    return () => {
      streamRef.current?.cancel();
      streamRef.current = null;
    };
  }, []);

  const start = useCallback((input: DebugInput) => {
    streamRef.current?.cancel();
    dispatch({ type: 'start' });
    streamRef.current = streamAIDebug(input, {
      onEvent: (event) => dispatch({ type: 'event', event }),
      onClose: (reason, error) =>
        dispatch({ type: 'close', reason, error: error?.message }),
    });
  }, []);

  const cancel = useCallback(() => {
    streamRef.current?.cancel();
    streamRef.current = null;
  }, []);

  const reset = useCallback(() => {
    streamRef.current?.cancel();
    streamRef.current = null;
    dispatch({ type: 'reset' });
  }, []);

  const approve = useCallback(
    async (decision: ApprovalDecision) => {
      const sessionId = state.sessionId;
      const requestId = state.pendingApproval?.data.request_id;
      if (!sessionId || !requestId) {
        throw new Error('no pending approval');
      }
      await postApproval(sessionId, requestId, decision);
    },
    [state.sessionId, state.pendingApproval?.data.request_id]
  );

  return { state, start, cancel, reset, approve };
}
