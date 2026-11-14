import type { AuditEntry } from '@/api/queries';

const ACTIONS: Record<string, string> = {
  'session.prompt': 'Prompted',
  'session.cancel': 'Cancelled a turn',
  'session.permission': 'Answered a permission request',
  'session.create': 'Created a session',
  'session.delete': 'Deleted a session',
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
  'agent.rebuild': 'Rebuilt the agent',
  'grant.set': 'Shared',
  'grant.delete': 'Revoked access',
  'network_policy.set': 'Changed the network policy',
  'secret.attach': 'Attached a secret',
  'secret.detach': 'Detached a secret',
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
  if (e.action.startsWith('grant.')) {
    const subject =
      s(d.subject) ||
      s(d.userName) ||
      s(d.user) ||
      (d.subjectType === 'workspace' ? 'everyone in the workspace' : '');
    return [s(d.role) && `role: ${s(d.role)}`, subject && `to: ${subject}`]
      .filter(Boolean)
      .join(', ');
  }
  const t = s(d.text);
  return t.length > MAX_TEXT ? `${t.slice(0, MAX_TEXT)}…` : t;
}

export const VIA_LABEL: Record<string, string> = {
  owner: 'Owner',
  workspace_admin: 'Workspace admin',
  grant: 'Granted',
  workspace_grant: 'Everyone in workspace',
};

export const ROLE_HELP: Record<string, string> = {
  viewer: 'Can read sessions, files and the activity they are part of.',
  operator:
    'Can also prompt, cancel, answer permissions, use the terminal and start or stop the agent.',
  admin:
    'Can also change settings, secrets and sharing, view activity and delete the agent.',
};
