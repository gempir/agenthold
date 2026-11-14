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
