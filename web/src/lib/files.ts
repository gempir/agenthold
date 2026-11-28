export function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

export function joinPath(dir: string, name: string): string {
  return dir.endsWith('/') ? dir + name : `${dir}/${name}`;
}

/** Breadcrumb segments for an absolute path, each with its cumulative path. */
export function breadcrumbs(abs: string): { name: string; path: string }[] {
  const out = [{ name: '/', path: '/' }];
  let acc = '';
  for (const part of abs.split('/').filter(Boolean)) {
    acc += `/${part}`;
    out.push({ name: part, path: acc });
  }
  return out;
}

export function contentUrl(agentId: string, path: string): string {
  return `/api/v1/agents/${encodeURIComponent(agentId)}/files/content?path=${encodeURIComponent(path)}`;
}

/** The agent's home directory inside its sandbox. */
export const AGENT_HOME = '/home/agent';

/**
 * A path relative to the agent's home, as the file and git APIs want it:
 * "~/x", "/home/agent/x" and "x" all become "x". A path outside the home
 * stays absolute.
 */
export function homeRel(p: string): string {
  let s = p.trim();
  if (s === '~' || s === AGENT_HOME || s === `${AGENT_HOME}/`) return '';
  if (s.startsWith('~/')) s = s.slice(2);
  else if (s.startsWith(`${AGENT_HOME}/`)) s = s.slice(AGENT_HOME.length + 1);
  // Normalize "./" and duplicate slashes; ".." is left for the server to refuse.
  const abs = s.startsWith('/');
  const parts = s.split('/').filter((x) => x && x !== '.');
  return (abs ? '/' : '') + parts.join('/');
}

/** Joins home-relative paths, ignoring empty parts. */
export function joinRel(...parts: (string | undefined)[]): string {
  return homeRel(parts.filter(Boolean).join('/'));
}

/** The last path segment. */
export const baseName = (p: string) => p.split('/').filter(Boolean).pop() ?? p;

/** The directory of a path ('' for a top-level one). */
export const dirName = (p: string) => {
  const i = p.lastIndexOf('/');
  return i <= 0 ? (i === 0 ? '/' : '') : p.slice(0, i);
};

/** The lowercased extension, without the dot. */
export const extOf = (p: string) => {
  const b = baseName(p);
  const i = b.lastIndexOf('.');
  return i > 0 ? b.slice(i + 1).toLowerCase() : '';
};

export type FileRef = { path: string; line?: number };

const SCHEME = /^[a-z][a-z0-9+.-]*:/i;
// "main.go:42" reads like a scheme to a URL parser but names a line.
const LINE_REF = /^[^/:#?]+:\d+(?::\d+)?(?:#.*)?$/;

/**
 * A link to a workspace file, as agents write them: "file:///home/agent/x.go",
 * "/home/agent/x.go:42", "src/x.go#L42" or "./x.go". Null for web links,
 * anchors and other schemes. Line suffixes: ":42", ":42:7", "#L42", "#L42-L50".
 */
export function parseFileRef(href: string | undefined): FileRef | null {
  if (!href) return null;
  let s = href.trim();
  if (!s || s.startsWith('#') || s.startsWith('//')) return null;
  if (/^file:/i.test(s)) {
    s = s.replace(/^file:(\/\/)?/i, '');
    if (!s.startsWith('/')) s = `/${s}`;
  } else if (SCHEME.test(s) && !LINE_REF.test(s)) {
    return null;
  }
  try {
    s = decodeURI(s);
  } catch {
    // keep it as written
  }
  let line: number | undefined;
  const hash = s.match(/#L(\d+)(?:-L?\d+)?$/i);
  if (hash) {
    line = Number(hash[1]);
    s = s.slice(0, hash.index);
  } else {
    s = s.replace(/#.*$/, '');
  }
  const suffix = s.match(/:(\d+)(?::\d+)?$/);
  if (suffix) {
    line ??= Number(suffix[1]);
    s = s.slice(0, suffix.index);
  }
  s = s.replace(/\?.*$/, '');
  if (!s || s.endsWith('/')) return null;
  // A bare word ("foo") is more likely a broken link than a file; need a
  // directory or an extension.
  if (!s.includes('/') && !s.includes('.')) return null;
  return { path: s, ...(line && line > 0 && { line }) };
}

/** A file path relative to the home, resolving a relative one against cwd. */
export function resolveFileRef(path: string, cwd?: string): string {
  if (path.startsWith('/') || path.startsWith('~')) return homeRel(path);
  return joinRel(cwd, path);
}
