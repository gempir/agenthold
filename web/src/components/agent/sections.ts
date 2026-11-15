import {
  Activity,
  Cpu,
  FolderOpen,
  Globe,
  Plug,
  Settings2,
  Share2,
  SquareTerminal,
  Zap,
  type LucideIcon,
} from 'lucide-react';

export type AgentSection =
  | 'general'
  | 'harness'
  | 'files'
  | 'terminal'
  | 'network'
  | 'tools'
  | 'triggers'
  | 'sharing'
  | 'activity';

export interface Access {
  operator: boolean;
  admin: boolean;
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
    hint: 'Harness, model, permissions, sign-in',
    icon: Cpu,
    show: () => true,
  },
  {
    id: 'files',
    label: 'Files',
    hint: 'Browse the home directory',
    icon: FolderOpen,
    show: () => true,
  },
  {
    id: 'terminal',
    label: 'Terminal',
    hint: 'A shell in the sandbox',
    icon: SquareTerminal,
    show: (a) => a.operator,
  },
  {
    id: 'network',
    label: 'Network',
    hint: 'Egress policy, secrets, request log',
    icon: Globe,
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
    hint: 'Who can view and operate it',
    icon: Share2,
    show: () => true,
  },
  {
    id: 'activity',
    label: 'Activity',
    hint: 'Audit log',
    icon: Activity,
    show: (a) => a.admin,
  },
];

export const isAgentSection = (v: unknown): v is AgentSection =>
  AGENT_SECTIONS.some((s) => s.id === v);
