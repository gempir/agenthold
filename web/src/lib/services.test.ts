import { expect, it } from 'vitest';
import { appendLog, serviceLogsUrl, streamError } from './services';

it('builds the log stream URL', () => {
  expect(
    serviceLogsUrl({ protocol: 'https:', host: 'h' }, 'ag 1', 'web', 50),
  ).toBe('wss://h/api/v1/agents/ag%201/services/web/logs/stream?lines=50');
});

it('caps the log at a line start', () => {
  expect(appendLog('aaa\nbbb\n', 'ccc\n', 8)).toBe('ccc\n');
  expect(appendLog('a\n', 'b\n')).toBe('a\nb\n');
});

it('recognises stream errors', () => {
  expect(streamError('{"type":"error","message":"gone"}')).toBe('gone');
  expect(streamError('plain log line')).toBeNull();
});
