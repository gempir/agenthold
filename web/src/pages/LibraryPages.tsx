import { AgentsSection } from '@/components/AgentsSection';
import { SecretsPanel } from '@/components/SecretsPanel';
import { LibraryToolsPanel } from '@/components/ToolsPanel';
import { SettingsHeader } from '@/components/layout/SettingsLayout';

/** Account settings: your agents and agents shared with you. */
export function AgentsSettingsPage() {
  return (
    <>
      <SettingsHeader
        title="Agents"
        description="Agents you own and agents shared with you"
      />
      <AgentsSection />
    </>
  );
}

/** Account settings: your secrets, attachable to agents you own. */
export function SecretsSettingsPage() {
  return (
    <>
      <SettingsHeader
        title="Secrets"
        description="Credentials the egress proxy injects for your agents"
      />
      <SecretsPanel />
    </>
  );
}

/** Account settings: your MCP server and skills libraries. */
export function ToolsSettingsPage() {
  return (
    <>
      <SettingsHeader
        title="Tools"
        description="MCP servers and skills you can attach to your agents"
      />
      <LibraryToolsPanel />
    </>
  );
}
