import {
  Activity,
  Cpu,
  FolderOpen,
  Globe,
  KeyRound,
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
  | 'preview'
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
    show: (a) => a.canUse,
  },
  {
    id: 'preview',
    label: 'Preview',
    hint: 'Ports of the machine at preview URLs',
    icon: Globe,
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
    label: 'Activity',
    hint: 'Audit log',
    icon: Activity,
    show: (a) => a.isOwner,
  },
];

export const isAgentSection = (v: unknown): v is AgentSection =>
  AGENT_SECTIONS.some((s) => s.id === v);
