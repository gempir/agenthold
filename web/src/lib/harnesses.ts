/**
 * What the web UI knows of harnesses and model providers by id, for icons
 * and names where the server's list is not at hand. Unknown ids fall back to
 * a generic icon and the id itself, so a new harness works before it is
 * listed here.
 */

/** The logos the UI has. */
export type Brand = 'anthropic' | 'openai' | 'opencode' | 'google' | 'generic';

const HARNESSES: Record<string, { name: string; brand: Brand }> = {
  'claude-code': { name: 'Claude Code', brand: 'anthropic' },
  codex: { name: 'Codex', brand: 'openai' },
  opencode: { name: 'OpenCode', brand: 'opencode' },
  antigravity: { name: 'Antigravity', brand: 'google' },
};

/** The display name of a harness id ("claude-code" → "Claude Code"). */
export const harnessLabel = (id: string) => HARNESSES[id]?.name ?? id;

/** The logo of a harness. */
export const harnessBrand = (id: string): Brand =>
  HARNESSES[id]?.brand ?? 'generic';

/**
 * The provider behind a model, from its group, id or name ("Anthropic",
 * "anthropic/claude-sonnet-5-5", "gpt-5"); undefined when nothing says.
 */
export function providerBrand(...hints: (string | undefined)[]) {
  const s = hints.filter(Boolean).join(' ').toLowerCase();
  if (/anthropic|claude|opus|sonnet|haiku/.test(s)) return 'anthropic';
  if (/openai|chatgpt|gpt|codex|\bo[1-9]\b/.test(s)) return 'openai';
  if (/google|gemini|antigravity/.test(s)) return 'google';
  return undefined;
}
