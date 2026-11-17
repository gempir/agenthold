/** WebSocket URL following a service's log. */
export function serviceLogsUrl(
  loc: { protocol: string; host: string },
  agentId: string,
  name: string,
  lines = 200,
): string {
  const scheme = loc.protocol === 'https:' ? 'wss' : 'ws';
  return `${scheme}://${loc.host}/api/v1/agents/${encodeURIComponent(agentId)}/services/${encodeURIComponent(name)}/logs/stream?lines=${lines}`;
}

/** Keeps the last max characters of a growing log, cut at a line start. */
export function appendLog(prev: string, chunk: string, max = 200_000): string {
  const next = prev + chunk;
  if (next.length <= max) return next;
  const cut = next.slice(next.length - max);
  const nl = cut.indexOf('\n');
  return nl >= 0 ? cut.slice(nl + 1) : cut;
}

/** An error message the log stream sends as JSON, or null for log text. */
export function streamError(text: string): string | null {
  if (!text.startsWith('{"type":"error"')) return null;
  try {
    const m = JSON.parse(text) as { message?: unknown };
    return String(m.message ?? 'error');
  } catch {
    return null;
  }
}
