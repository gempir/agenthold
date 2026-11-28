/** One line of a parsed unified diff. */
export type PatchRow =
  | { kind: 'hunk'; text: string }
  | {
      kind: 'context' | 'add' | 'del';
      text: string;
      /** Line number in the old file (context and deletions). */
      oldNo?: number;
      /** Line number in the new file (context and additions). */
      newNo?: number;
    };

const HUNK = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/;

/**
 * The rows of a unified diff of one file: hunk headers and numbered lines.
 * File headers ("diff --git", "---", "+++", "index") are skipped.
 */
export function parsePatch(patch: string | undefined): PatchRow[] {
  if (!patch) return [];
  const rows: PatchRow[] = [];
  let oldNo = 0;
  let newNo = 0;
  let inHunk = false;
  const lines = patch.split('\n');
  if (lines[lines.length - 1] === '') lines.pop();
  for (const line of lines) {
    const h = line.match(HUNK);
    if (h) {
      oldNo = Number(h[1]);
      newNo = Number(h[2]);
      inHunk = true;
      rows.push({ kind: 'hunk', text: line });
      continue;
    }
    if (!inHunk) continue;
    const mark = line[0];
    const text = line.slice(1);
    if (mark === '+') rows.push({ kind: 'add', text, newNo: newNo++ });
    else if (mark === '-') rows.push({ kind: 'del', text, oldNo: oldNo++ });
    else if (mark === ' ' || line === '')
      rows.push({ kind: 'context', text, oldNo: oldNo++, newNo: newNo++ });
    // "\ No newline at end of file" and anything else: not a line.
  }
  return rows;
}

type LineRow = Exclude<PatchRow, { kind: 'hunk' }>;

/** A row of the side-by-side layout: the old line left, the new right. */
export type SplitRow =
  | { kind: 'hunk'; text: string; index: number }
  | {
      kind: 'pair';
      left?: LineRow;
      right?: LineRow;
      /** Positions of left and right in the unified rows. */
      leftIndex?: number;
      rightIndex?: number;
      index: number;
    };

/**
 * Pairs the rows for a side-by-side view: context on both sides, and each run
 * of deletions next to the additions that follow it. index is the position of
 * the row's first line in the unified rows.
 */
export function splitRows(rows: PatchRow[]): SplitRow[] {
  const out: SplitRow[] = [];
  let i = 0;
  while (i < rows.length) {
    const r = rows[i];
    if (r.kind === 'hunk') {
      out.push({ kind: 'hunk', text: r.text, index: i });
      i++;
    } else if (r.kind === 'context') {
      out.push({
        kind: 'pair',
        left: r,
        right: r,
        leftIndex: i,
        rightIndex: i,
        index: i,
      });
      i++;
    } else {
      const start = i;
      const dels: number[] = [];
      const adds: number[] = [];
      while (i < rows.length && rows[i].kind === 'del') dels.push(i++);
      while (i < rows.length && rows[i].kind === 'add') adds.push(i++);
      for (let k = 0; k < Math.max(dels.length, adds.length); k++)
        out.push({
          kind: 'pair',
          left: dels[k] === undefined ? undefined : (rows[dels[k]] as LineRow),
          right: adds[k] === undefined ? undefined : (rows[adds[k]] as LineRow),
          leftIndex: dels[k],
          rightIndex: adds[k],
          index: start,
        });
    }
  }
  return out;
}

/** The side and line number a diff row is addressed by in a comment. */
export function rowLine(
  r: PatchRow,
): { side: 'old' | 'new'; line: number } | null {
  if (r.kind === 'hunk') return null;
  if (r.kind === 'del') return { side: 'old', line: r.oldNo ?? 0 };
  return { side: 'new', line: r.newNo ?? 0 };
}
