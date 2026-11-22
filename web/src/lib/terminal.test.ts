import { expect, it } from 'bun:test';
import { parseServerMessage, terminalUrl } from './terminal';

it('builds ws/wss urls', () => {
  expect(terminalUrl({ protocol: 'http:', host: 'h:1' }, 'a1', 80, 24)).toBe(
    'ws://h:1/api/v1/agents/a1/terminal?cols=80&rows=24',
  );
  expect(terminalUrl({ protocol: 'https:', host: 'h' }, 'a1', 1, 2)).toMatch(
    /^wss:\/\/h\//,
  );
});

it('appends the auth method', () => {
  expect(
    terminalUrl({ protocol: 'http:', host: 'h' }, 'a1', 1, 2, 'claude-login'),
  ).toMatch(/&auth=claude-login$/);
});

it('parses server messages', () => {
  expect(parseServerMessage('{"type":"exit","code":3}')).toEqual({
    type: 'exit',
    code: 3,
  });
  expect(parseServerMessage('{"type":"status","message":"waking"}')).toEqual({
    type: 'status',
    message: 'waking',
  });
  expect(parseServerMessage('nope')).toBeNull();
  expect(parseServerMessage('{"type":"x"}')).toBeNull();
});
