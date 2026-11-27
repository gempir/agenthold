import { expect, it } from 'bun:test';
import { documentTitle } from './useDocumentTitle';

it('joins the parts it has, or falls back to the app name', () => {
  expect(documentTitle('Fix the build', 'builder')).toBe(
    'Fix the build · builder',
  );
  expect(documentTitle(undefined, 'builder')).toBe('builder');
  expect(documentTitle('  ', null)).toBe('agenthold');
});
