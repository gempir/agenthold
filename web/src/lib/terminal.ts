export function terminalUrl(
  loc: { protocol: string; host: string },
  agentId: string,
  cols: number,
  rows: number,
): string {
  const scheme = loc.protocol === 'https:' ? 'wss' : 'ws';
  return `${scheme}://${loc.host}/api/v1/agents/${encodeURIComponent(agentId)}/terminal?cols=${cols}&rows=${rows}`;
}

export type ServerMessage =
  | { type: 'status'; message: string }
  | { type: 'exit'; code: number }
  | { type: 'error'; message: string };

/** Parses a server text frame; returns null for anything unrecognised. */
export function parseServerMessage(text: string): ServerMessage | null {
  try {
    const m: unknown = JSON.parse(text);
    if (!m || typeof m !== 'object') return null;
    const o = m as Record<string, unknown>;
    if (o.type === 'status' || o.type === 'error')
      return { type: o.type, message: String(o.message ?? '') };
    if (o.type === 'exit') return { type: 'exit', code: Number(o.code ?? 0) };
  } catch {
    // not JSON
  }
  return null;
}
