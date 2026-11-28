/**
 * A small, dependency-free highlighter: comments, strings, numbers and
 * keywords for the languages agents usually write. It colors line by line
 * (carrying block comments across lines), which is all a code view and chat
 * code blocks need.
 */
export type TokenKind = 'plain' | 'comment' | 'string' | 'number' | 'keyword';
export type Token = { kind: TokenKind; text: string };

type Lang = {
  line?: string[];
  block?: [string, string];
  strings: string[];
  keywords: Set<string>;
};

const words = (s: string) => new Set(s.split(/\s+/).filter(Boolean));

const C_LIKE_BLOCK: [string, string] = ['/*', '*/'];
const JS = words(`
  async await break case catch class const continue debugger default delete do
  else enum export extends false finally for from function if implements import
  in instanceof interface let new null of package private protected public
  readonly return satisfies static super switch this throw true try type
  typeof undefined var void while with yield as declare keyof namespace`);
const GO = words(`
  break case chan const continue default defer else fallthrough for func go
  goto if import interface map package range return select struct switch type
  var nil true false iota any error string int int64 int32 uint uint64 byte
  rune bool float64 float32 make new len cap append panic recover`);
const PY = words(`
  and as assert async await break class continue def del elif else except
  False finally for from global if import in is lambda None nonlocal not or
  pass raise return True try while with yield self match case`);
const RUST = words(`
  as async await break const continue crate dyn else enum extern false fn for
  if impl in let loop match mod move mut pub ref return self Self static struct
  super trait true type unsafe use where while Some None Ok Err`);
const SH = words(`
  if then else elif fi for while until do done case esac in function return
  local export readonly set unset echo exit source true false`);
const SQL = words(`
  select from where insert into values update set delete create table alter
  drop index primary key foreign references not null and or join left right
  inner outer on group by order having limit offset as distinct union all case
  when then else end default unique returning begin commit`);
const JSONW = words('true false null');
const CSS = words('important inherit initial unset none auto');

const LANGS: Record<string, Lang> = {
  js: {
    line: ['//'],
    block: C_LIKE_BLOCK,
    strings: ['"', "'", '`'],
    keywords: JS,
  },
  go: {
    line: ['//'],
    block: C_LIKE_BLOCK,
    strings: ['"', "'", '`'],
    keywords: GO,
  },
  py: { line: ['#'], strings: ['"', "'"], keywords: PY },
  rust: { line: ['//'], block: C_LIKE_BLOCK, strings: ['"'], keywords: RUST },
  sh: { line: ['#'], strings: ['"', "'"], keywords: SH },
  sql: {
    line: ['--'],
    block: C_LIKE_BLOCK,
    strings: ["'", '"'],
    keywords: SQL,
  },
  json: { strings: ['"'], keywords: JSONW },
  yaml: {
    line: ['#'],
    strings: ['"', "'"],
    keywords: words('true false null yes no'),
  },
  css: { block: C_LIKE_BLOCK, strings: ['"', "'"], keywords: CSS },
  c: {
    line: ['//'],
    block: C_LIKE_BLOCK,
    strings: ['"', "'"],
    keywords: words(`
      auto break case char const continue default do double else enum extern
      float for goto if int long register return short signed sizeof static
      struct switch typedef union unsigned void volatile while class public
      private protected namespace template typename new delete true false
      nullptr this virtual override final import package boolean extends
      implements interface throws throw try catch finally null fun val var`),
  },
  html: { block: ['<!--', '-->'], strings: ['"', "'"], keywords: new Set() },
};

const ALIASES: Record<string, string> = {
  javascript: 'js',
  jsx: 'js',
  mjs: 'js',
  cjs: 'js',
  ts: 'js',
  tsx: 'js',
  typescript: 'js',
  mts: 'js',
  cts: 'js',
  golang: 'go',
  python: 'py',
  rs: 'rust',
  bash: 'sh',
  zsh: 'sh',
  shell: 'sh',
  console: 'sh',
  fish: 'sh',
  yml: 'yaml',
  toml: 'yaml',
  ini: 'yaml',
  jsonc: 'json',
  json5: 'json',
  scss: 'css',
  less: 'css',
  h: 'c',
  cpp: 'c',
  cc: 'c',
  hpp: 'c',
  cs: 'c',
  java: 'c',
  kt: 'c',
  kotlin: 'c',
  swift: 'c',
  csharp: 'c',
  'c++': 'c',
  xml: 'html',
  svg: 'html',
  vue: 'html',
  svelte: 'html',
  htm: 'html',
  dockerfile: 'sh',
  makefile: 'sh',
  mk: 'sh',
  psql: 'sql',
  postgres: 'sql',
};

/** The highlighter language of a fence label or file extension (null: none). */
export function langOf(nameOrExt: string | undefined): string | null {
  if (!nameOrExt) return null;
  const k = nameOrExt.toLowerCase();
  if (LANGS[k]) return k;
  return ALIASES[k] ?? null;
}

/** The language of a file by its name. */
export function langOfPath(path: string): string | null {
  const base = path.split('/').pop()?.toLowerCase() ?? '';
  if (base === 'dockerfile' || base === 'makefile') return 'sh';
  if (base.startsWith('.') && !base.slice(1).includes('.')) return 'sh';
  const i = base.lastIndexOf('.');
  return i > 0 ? langOf(base.slice(i + 1)) : null;
}

/** Carried from one line to the next: inside a block comment. */
export type HighlightState = { inBlock: boolean };

const NUM = /^(?:0[xX][\da-fA-F_]+|\d[\d_]*(?:\.\d+)?(?:[eE][+-]?\d+)?)/;
const WORD = /^[A-Za-z_$][\w$]*/;

/** Tokens of one line; state is updated for the next line. */
export function highlightLine(
  line: string,
  lang: string | null,
  state: HighlightState = { inBlock: false },
): Token[] {
  const l = lang ? LANGS[lang] : undefined;
  if (!l) return [{ kind: 'plain', text: line }];
  const out: Token[] = [];
  const push = (kind: TokenKind, text: string) => {
    if (!text) return;
    const last = out[out.length - 1];
    if (last && last.kind === kind) last.text += text;
    else out.push({ kind, text });
  };
  let i = 0;
  if (state.inBlock && l.block) {
    const end = line.indexOf(l.block[1]);
    if (end < 0) return [{ kind: 'comment', text: line }];
    push('comment', line.slice(0, end + l.block[1].length));
    i = end + l.block[1].length;
    state.inBlock = false;
  }
  while (i < line.length) {
    const rest = line.slice(i);
    if (l.line?.some((m) => rest.startsWith(m))) {
      push('comment', rest);
      break;
    }
    if (l.block && rest.startsWith(l.block[0])) {
      const end = rest.indexOf(l.block[1], l.block[0].length);
      if (end < 0) {
        push('comment', rest);
        state.inBlock = true;
        break;
      }
      push('comment', rest.slice(0, end + l.block[1].length));
      i += end + l.block[1].length;
      continue;
    }
    const q = rest[0];
    if (l.strings.includes(q)) {
      let j = 1;
      while (j < rest.length && rest[j] !== q) j += rest[j] === '\\' ? 2 : 1;
      push('string', rest.slice(0, j + 1));
      i += j + 1;
      continue;
    }
    const prev = line[i - 1];
    if (/\d/.test(q) && !(prev && /[\w$]/.test(prev))) {
      const m = rest.match(NUM);
      if (m) {
        push('number', m[0]);
        i += m[0].length;
        continue;
      }
    }
    const w = rest.match(WORD);
    if (w) {
      push(l.keywords.has(w[0]) ? 'keyword' : 'plain', w[0]);
      i += w[0].length;
      continue;
    }
    push('plain', q);
    i++;
  }
  return out;
}

/** Tokens of every line of a text. */
export function highlightLines(text: string, lang: string | null): Token[][] {
  const state: HighlightState = { inBlock: false };
  return text.split('\n').map((line) => highlightLine(line, lang, state));
}

export const TOKEN_CLASS: Record<TokenKind, string> = {
  plain: '',
  comment: 'text-muted-foreground italic',
  string: 'text-emerald-700 dark:text-emerald-400',
  number: 'text-amber-700 dark:text-amber-400',
  keyword: 'text-violet-700 dark:text-violet-400',
};
