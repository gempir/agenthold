import type { AuditEntry } from '@/api/queries';

const ACTIONS: Record<string, string> = {
  'session.prompt': 'Prompted',
  'session.cancel': 'Cancelled a turn',
  'session.permission': 'Answered a permission request',
  'session.create': 'Started a thread',
  'session.delete': 'Deleted a thread',
  'session.settle': 'Settled a thread',
  'session.unsettle': 'Unsettled a thread',
  'terminal.open': 'Opened a terminal',
  'harness.login': 'Logged in the harness',
  'file.write': 'Uploaded a file',
  'file.delete': 'Deleted a file',
  'file.mkdir': 'Created a folder',
  'agent.create': 'Created the agent',
  'agent.update': 'Changed settings',
  'agent.delete': 'Deleted the agent',
  'agent.start': 'Started the agent',
  'agent.stop': 'Stopped the agent',
  'agent.rebuild': 'Reset the agent system',
  'share.add': 'Shared',
  'share.remove': 'Stopped sharing',
  'secret.attach': 'Attached a secret',
  'secret.detach': 'Detached a secret',
  'exposure.add': 'Exposed a port',
  'exposure.update': 'Changed an exposure',
  'exposure.remove': 'Removed an exposure',
};

/** Human wording for an audit action; the raw action if unknown. */
export const humanizeAction = (action: string): string =>
  ACTIONS[action] ?? action;

const MAX_TEXT = 80;
const s = (v: unknown): string => (typeof v === 'string' ? v : '');

/** One-line summary of an audit entry's details. */
export function describeDetails(e: Pick<AuditEntry, 'action' | 'details'>) {
  const d = (e.details ?? {}) as Record<string, unknown>;
  if (e.action === 'session.prompt') {
    const t = s(d.text);
    return t.length > MAX_TEXT ? `${t.slice(0, MAX_TEXT)}…` : t;
  }
  if (e.action === 'session.settle' || e.action === 'session.unsettle') {
    const why =
      d.reason === 'auto'
        ? `automatically, after ${String(d.afterDays)} idle days`
        : d.reason === 'activity'
          ? 'by a new message'
          : s(d.fromAgent)
            ? `by the agent ${s(d.fromAgent)}`
            : '';
    return [s(d.session), why].filter(Boolean).join(', ');
  }
  if (e.action.startsWith('share.')) return s(d.email) || s(d.userId);
  if (e.action.startsWith('exposure.'))
    return [s(d.name), typeof d.port === 'number' ? `port ${d.port}` : '']
      .filter(Boolean)
      .join(', ');
  const t = s(d.text);
  return t.length > MAX_TEXT ? `${t.slice(0, MAX_TEXT)}…` : t;
}

export const SHARED_HELP =
  'People you share with can start threads with the agent, answer its permission requests, use its terminal and files, and start or stop it. Only you can change its settings, tools, secrets and sharing, or delete it.';
