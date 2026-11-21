import type { components } from '@/api/schema';

type NodeInfo = components['schemas']['NodeInfo'];

const MIB = 1024 ** 2;
const GIB = 1024 ** 3;

function size(bytes: number) {
  return bytes < GIB
    ? `${Math.round(bytes / MIB)} MiB`
    : `${(bytes / GIB).toFixed(1)} GiB`;
}

/** Whether a new agent with `memoryMb` of memory can be placed on the node,
 * and the reason when not. The server decides again; this only keeps the
 * picker from offering what it would refuse. */
export function nodeChoice(
  n: NodeInfo,
  memoryMb: number,
): { disabled: boolean; reason: string } {
  if (!n.online) return { disabled: true, reason: `${n.name} is offline.` };
  if (n.availableMemoryBytes === undefined)
    return {
      disabled: true,
      reason: `${n.name} has not reported its capacity.`,
    };
  if (n.availableMemoryBytes < memoryMb * MIB)
    return {
      disabled: true,
      reason: `${n.name} has ${size(n.availableMemoryBytes)} of memory available; this agent needs ${memoryMb} MB. Choose another node or a smaller agent.`,
    };
  return { disabled: false, reason: '' };
}

/** One line for the node picker: name, status and what is free. */
export function nodeLabel(n: NodeInfo, memoryMb: number): string {
  const { disabled } = nodeChoice(n, memoryMb);
  const status = !n.online
    ? 'offline'
    : n.availableMemoryBytes === undefined
      ? 'capacity unknown'
      : disabled
        ? 'not enough memory'
        : 'online';
  const free = [
    n.availableMemoryBytes !== undefined &&
      `${size(n.availableMemoryBytes)} memory free`,
    n.freeDiskBytes !== undefined && `${size(n.freeDiskBytes)} disk free`,
  ].filter(Boolean);
  const agents = `${n.agents} ${n.agents === 1 ? 'agent' : 'agents'}`;
  return [n.name, status, ...free, agents].join(' · ');
}
