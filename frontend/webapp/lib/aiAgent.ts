import { API } from '@/utils';
import type { AgentEvent, ApprovalDecision, DebugInput } from '@/types';

// SSE consumer for POST /api/ai/debug. EventSource doesn't support POST, so we
// drive a fetch + ReadableStream + tiny SSE parser. The Go backend forwards to
// the in-cluster agent verbatim, so the wire format matches sse-starlette's
// default: blank-line separated event blocks of `event: <name>` and `data: <json>` lines.

export interface StreamHandle {
  cancel: () => void;
}

export type StreamCloseReason = 'done' | 'aborted' | 'error';

export interface StreamCallbacks {
  onEvent: (event: AgentEvent) => void;
  onClose?: (reason: StreamCloseReason, error?: Error) => void;
}

function parseEventBlock(block: string): AgentEvent | null {
  let eventName = '';
  const dataLines: string[] = [];
  for (const rawLine of block.split('\n')) {
    const line = rawLine.replace(/\r$/, '');
    if (!line || line.startsWith(':')) continue;
    if (line.startsWith('event:')) {
      eventName = line.slice(6).trim();
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).replace(/^ /, ''));
    }
  }
  if (!eventName) return null;
  const dataText = dataLines.join('\n');
  let data: unknown = {};
  if (dataText) {
    try {
      data = JSON.parse(dataText);
    } catch {
      data = { raw: dataText };
    }
  }
  return { event: eventName, data } as AgentEvent;
}

export function streamAIDebug(input: DebugInput, callbacks: StreamCallbacks): StreamHandle {
  const controller = new AbortController();
  let closed = false;

  const close = (reason: StreamCloseReason, error?: Error) => {
    if (closed) return;
    closed = true;
    callbacks.onClose?.(reason, error);
  };

  (async () => {
    try {
      const response = await fetch(`${API.BACKEND_HTTP_ORIGIN}/api/ai/debug`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Accept: 'text/event-stream',
        },
        body: JSON.stringify(input),
        signal: controller.signal,
        credentials: 'include',
      });

      if (!response.ok) {
        let detail = '';
        try {
          detail = await response.text();
        } catch {
          // ignore
        }
        close('error', new Error(`agent /debug returned ${response.status}: ${detail || '<no body>'}`));
        return;
      }
      if (!response.body) {
        close('error', new Error('agent /debug response had no body'));
        return;
      }

      const reader = response.body.getReader();
      const decoder = new TextDecoder('utf-8');
      let buffer = '';

      // SSE event blocks are separated by a blank line - "\n\n" (or "\r\n\r\n").
      // We accumulate bytes into `buffer`, split on blank lines, parse the head
      // blocks, and keep the tail as the in-progress block.
      // eslint-disable-next-line no-constant-condition
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        buffer = buffer.replace(/\r\n/g, '\n');
        let separatorIdx = buffer.indexOf('\n\n');
        while (separatorIdx !== -1) {
          const block = buffer.slice(0, separatorIdx);
          buffer = buffer.slice(separatorIdx + 2);
          const parsed = parseEventBlock(block);
          if (parsed) callbacks.onEvent(parsed);
          separatorIdx = buffer.indexOf('\n\n');
        }
      }
      // Flush any trailing block (no trailing blank line - rare but possible).
      const tail = buffer.trim();
      if (tail) {
        const parsed = parseEventBlock(tail);
        if (parsed) callbacks.onEvent(parsed);
      }
      close('done');
    } catch (err) {
      if ((err as Error)?.name === 'AbortError') {
        close('aborted');
      } else {
        close('error', err as Error);
      }
    }
  })();

  return {
    cancel: () => {
      controller.abort();
    },
  };
}

export async function postApproval(sessionId: string, requestId: string, decision: ApprovalDecision): Promise<void> {
  const response = await fetch(
    `${API.BACKEND_HTTP_ORIGIN}/api/ai/approve/${encodeURIComponent(sessionId)}/${encodeURIComponent(requestId)}`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ decision }),
      credentials: 'include',
    }
  );
  if (!response.ok) {
    let detail = '';
    try {
      detail = await response.text();
    } catch {
      // ignore
    }
    throw new Error(`agent /approve returned ${response.status}: ${detail || '<no body>'}`);
  }
}
