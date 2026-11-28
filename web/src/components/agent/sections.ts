import {
  Activity,
  Cpu,
  KeyRound,
  Plug,
  Server,
  Settings2,
  Share2,
  Zap,
  type LucideIcon,
} from 'lucide-react';

export type AgentSection =
  | 'general'
  | 'harness'
  | 'services'
  | 'secrets'
  | 'tools'
  | 'triggers'
  | 'sharing'
  | 'activity';

export interface Access {
  canUse: boolean;
  isOwner: boolean;
}

/** Every secondary surface of an agent; what used to be its tabs. */
export const AGENT_SECTIONS: {
  id: AgentSection;
  label: string;
  hint: string;
  icon: LucideIcon;
  show: (a: Access) => boolean;
}[] = [
  {
    id: 'general',
    label: 'General',
    hint: 'State, resources, rename, delete',
    icon: Settings2,
    show: () => true,
  },
  {
    id: 'harness',
    label: 'Harness',
    hint: 'Harness, model, sign-in',
    icon: Cpu,
    show: () => true,
  },
  {
    id: 'services',
    label: 'Services',
    hint: 'Commands kept running in the machine (dev servers, workers)',
    icon: Server,
    show: () => true,
  },
  {
    id: 'secrets',
    label: 'Secrets',
    hint: 'Credentials the egress proxy injects for this agent',
    icon: KeyRound,
    show: () => true,
  },
  {
    id: 'tools',
    label: 'Tools',
    hint: 'MCP servers and skills',
    icon: Plug,
    show: () => true,
  },
  {
    id: 'triggers',
    label: 'Triggers',
    hint: 'Schedules and webhooks',
    icon: Zap,
    show: () => true,
  },
  {
    id: 'sharing',
    label: 'Sharing',
    hint: 'Who can use it',
    icon: Share2,
    show: () => true,
  },
  {
    id: 'activity',
    label: 'Audit log',
    hint: 'Who changed what',
    icon: Activity,
    show: (a) => a.isOwner,
  },
];

export const isAgentSection = (v: unknown): v is AgentSection =>
  AGENT_SECTIONS.some((s) => s.id === v);
