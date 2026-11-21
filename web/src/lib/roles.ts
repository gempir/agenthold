import type { components } from '@/api/schema';

export type Role = components['schemas']['Role'];
export type AgentQuota = components['schemas']['AgentQuota'];

export const ROLE_LABEL: Record<Role, string> = {
  admin: 'Admin',
  user: 'User',
  disabled: 'Disabled',
};

/** "2 of 2 agents used", or undefined without a limit. */
export function quotaText(q: AgentQuota | undefined): string | undefined {
  if (!q || q.max === undefined) return undefined;
  return `${q.used} of ${q.max} agent${q.max === 1 ? '' : 's'} used`;
}

/** Whether the quota allows no further agent. */
export function quotaFull(q: AgentQuota | undefined): boolean {
  return !!q && q.max !== undefined && q.used >= q.max;
}
