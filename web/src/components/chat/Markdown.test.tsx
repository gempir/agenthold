import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'bun:test';
import Markdown from '@/components/chat/Markdown';

it('opens workspace file links in the viewer', async () => {
  const onOpenFile = vi.fn();
  render(
    <Markdown
      onOpenFile={onOpenFile}
      text={[
        'Changed [main.go:42](file:///home/agent/web/main.go)',
        'and [app.ts](src/app.ts#L7) and [docs](https://example.com/x.md).',
      ].join(' ')}
    />,
  );
  await userEvent.click(screen.getByRole('link', { name: 'main.go:42' }));
  // The line comes from the link text when the href has none.
  expect(onOpenFile).toHaveBeenCalledWith('/home/agent/web/main.go', 42);
  await userEvent.click(screen.getByRole('link', { name: 'app.ts' }));
  expect(onOpenFile).toHaveBeenLastCalledWith('src/app.ts', 7);
  const web = screen.getByRole('link', { name: 'docs' });
  expect(web).toHaveAttribute('target', '_blank');
  expect(web).not.toHaveAttribute('data-file-link');
  expect(onOpenFile).toHaveBeenCalledTimes(2);
});

it('leaves file links alone without a viewer', () => {
  render(<Markdown text="[a](src/a.go:3)" />);
  expect(screen.getByRole('link', { name: 'a' })).not.toHaveAttribute(
    'data-file-link',
  );
});

it('labels and highlights fenced code and copies it', async () => {
  const writeText = vi.fn(() => Promise.resolve());
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText },
    configurable: true,
  });
  render(<Markdown text={'```ts\nconst x = 1;\n```\n\n```\nplain\n```'} />);
  const [ts, plain] = screen.getAllByTestId('code-block');
  expect(within(ts).getByText('ts')).toBeVisible();
  expect(within(ts).getByText('const')).toHaveClass('text-violet-700');
  expect(within(plain).getByText('text')).toBeVisible();
  await userEvent.click(within(ts).getByRole('button', { name: 'Copy code' }));
  expect(writeText).toHaveBeenCalledWith('const x = 1;');
  expect(
    await within(ts).findByRole('button', { name: 'Copied' }),
  ).toBeVisible();
});
