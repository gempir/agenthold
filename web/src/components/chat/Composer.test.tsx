import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it, vi } from 'bun:test';
import { useState } from 'react';
import { Composer, type ComposerSubmitOptions } from './Composer';
import {
  LARGE_PASTE_BYTES,
  toImagePayload,
  useAttachments,
} from '@/lib/attachments';
import {
  useComposerContext,
  clearComposerContext,
} from '@/lib/composerContext';
import { clearStashes } from '@/lib/composerStash';
import { setFollowUpMode } from '@/lib/followUpPref';

beforeEach(() => {
  localStorage.clear();
  clearStashes();
  clearComposerContext('test-session');
  setFollowUpMode('queue');
});

const png = () =>
  new File([new Uint8Array([137, 80, 78, 71])], 'shot.png', {
    type: 'image/png',
  });

function Harness({
  onSubmit,
  onBackgroundSubmit,
  onSlashCommand,
  onEditLatestQueued,
  running = false,
  sentHistory = [],
}: {
  onSubmit: (payload: {
    text: string;
    images: unknown;
    files: string[];
    chips: unknown[];
    opts?: ComposerSubmitOptions;
  }) => void;
  onBackgroundSubmit?: () => void;
  onSlashCommand?: (cmd: string) => void;
  onEditLatestQueued?: () => void;
  running?: boolean;
  sentHistory?: string[];
}) {
  const [text, setText] = useState('');
  const att = useAttachments();
  const ctx = useComposerContext('test-session');
  return (
    <Composer
      value={text}
      onChange={setText}
      running={running}
      sessionId="test-session"
      attachments={att.items}
      fileAttachments={att.files}
      contextChips={ctx.chips}
      attachError={att.error}
      onAttach={att.add}
      onRemoveAttachment={att.remove}
      onRemoveFileAttachment={att.removeFile}
      onLargePaste={(p) => void att.addLargePaste(p)}
      onInlineLargePaste={(f) => {
        if (f.pasteText) setText(f.pasteText);
        att.removeFile(f.id);
      }}
      onAddContextChip={ctx.add}
      onRemoveContextChip={ctx.remove}
      onSlashCommand={onSlashCommand}
      onEditLatestQueued={onEditLatestQueued}
      onBackgroundSubmit={onBackgroundSubmit}
      sentHistory={sentHistory}
      onSubmit={(opts) =>
        onSubmit({
          text,
          images: toImagePayload(att.items),
          files: att.files.map((f) => f.name),
          chips: ctx.chips,
          opts,
        })
      }
    />
  );
}

it('attaches a picked image, sends it without text, and removes it', async () => {
  const onSubmit = vi.fn();
  render(<Harness onSubmit={onSubmit} />);
  expect((screen.getByLabelText('Send') as HTMLButtonElement).disabled).toBe(
    true,
  );

  fireEvent.change(screen.getByLabelText('Choose files to attach'), {
    target: { files: [png()] },
  });
  const thumb = await screen.findByAltText('shot.png');
  // A data: URL: the CSP's img-src does not allow blob: URLs.
  expect(thumb.getAttribute('src')).toBe('data:image/png;base64,iVBORw==');
  fireEvent.click(screen.getByLabelText('Send'));
  expect(onSubmit).toHaveBeenCalledWith(
    expect.objectContaining({
      images: [{ mimeType: 'image/png', data: 'iVBORw==' }],
    }),
  );

  fireEvent.click(screen.getByLabelText('View shot.png'));
  expect(screen.getByRole('dialog')).toBeTruthy();
  fireEvent.click(screen.getByLabelText('Close'));

  fireEvent.click(screen.getByLabelText('Remove shot.png'));
  expect(screen.queryByAltText('shot.png')).toBeNull();
});

it('accepts general files via picker, paste, and drag-and-drop', async () => {
  const onSubmit = vi.fn();
  render(<Harness onSubmit={onSubmit} />);
  const box = screen.getByLabelText('Message');

  fireEvent.paste(box, { clipboardData: { files: [png()] } });
  await screen.findByAltText('shot.png');

  const pdf = new File(['%PDF-1.4'], 'spec.pdf', { type: 'application/pdf' });
  fireEvent.change(screen.getByLabelText('Choose files to attach'), {
    target: { files: [pdf] },
  });
  expect(await screen.findByText('spec.pdf')).toBeVisible();

  const json = new File(['{"ok":true}'], 'data.json', {
    type: 'application/json',
  });
  const form = box.closest('form')!;
  fireEvent.drop(form, {
    dataTransfer: { files: [json], types: ['Files'] },
  });
  expect(await screen.findByText('data.json')).toBeVisible();

  fireEvent.click(screen.getByLabelText('Send'));
  expect(onSubmit).toHaveBeenCalledWith(
    expect.objectContaining({
      files: ['spec.pdf', 'data.json'],
    }),
  );
});

it('converts >= 32 KiB text pastes into a virtual file chip with inline option', async () => {
  render(<Harness onSubmit={() => {}} />);
  const box = screen.getByLabelText('Message');
  const huge = 'a'.repeat(LARGE_PASTE_BYTES + 100);
  fireEvent.paste(box, {
    clipboardData: {
      files: [],
      getData: (t: string) => (t === 'text/plain' ? huge : ''),
    },
  });
  const chip = await screen.findByTestId('composer-file-chip');
  expect(chip).toHaveTextContent(/paste-.*\.txt/);
  expect(box).toHaveValue('');

  fireEvent.click(screen.getByRole('button', { name: 'Inline' }));
  expect(box).toHaveValue(huge);
  expect(screen.queryByTestId('composer-file-chip')).toBeNull();
});

it('allows queuing and steering while running, with Cmd+Enter sending the opposite mode', async () => {
  const onSubmit = vi.fn();
  render(<Harness running onSubmit={onSubmit} />);
  const box = screen.getByLabelText('Message');
  const user = userEvent.setup();

  await user.type(box, 'follow up');
  await user.keyboard('{Enter}');
  expect(onSubmit).toHaveBeenLastCalledWith(
    expect.objectContaining({ opts: { mode: 'queue' } }),
  );

  // Cmd/Ctrl+Enter sends the opposite action (steer when default is queue)
  fireEvent.keyDown(box, { key: 'Enter', metaKey: true });
  expect(onSubmit).toHaveBeenLastCalledWith(
    expect.objectContaining({ opts: { mode: 'steer' } }),
  );

  // Toggle follow-up mode pill to Steer
  await user.click(
    screen.getByRole('button', { name: 'Follow-up mode: Queue' }),
  );
  expect(
    screen.getByRole('button', { name: 'Follow-up mode: Steer' }),
  ).toBeVisible();
  await user.keyboard('{Enter}');
  expect(onSubmit).toHaveBeenLastCalledWith(
    expect.objectContaining({ opts: { mode: 'steer' } }),
  );
  fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true });
  expect(onSubmit).toHaveBeenLastCalledWith(
    expect.objectContaining({ opts: { mode: 'queue' } }),
  );
});

it('recalls sent prompts with ArrowUp/ArrowDown and stashes/restores with Cmd+S', async () => {
  render(
    <Harness onSubmit={() => {}} sentHistory={['first msg', 'second msg']} />,
  );
  const box = screen.getByLabelText('Message');
  const user = userEvent.setup();

  await user.click(box);
  await user.keyboard('{ArrowUp}');
  expect(box).toHaveValue('second msg');
  await user.keyboard('{ArrowUp}');
  expect(box).toHaveValue('first msg');
  await user.keyboard('{ArrowDown}');
  expect(box).toHaveValue('second msg');
  await user.keyboard('{ArrowDown}');
  expect(box).toHaveValue('');

  await user.type(box, 'work in progress');
  fireEvent.keyDown(box, { key: 's', metaKey: true });
  expect(box).toHaveValue('');
  expect(
    screen.getByRole('button', { name: 'Stashed prompts (1)' }),
  ).toBeVisible();

  // Cmd+S on empty composer restores latest stash
  fireEvent.keyDown(box, { key: 's', metaKey: true });
  expect(box).toHaveValue('work in progress');
});

it('triggers Option+Up to edit latest queued message and Cmd+Enter for background start', async () => {
  const onEditLatest = vi.fn();
  const onBg = vi.fn();
  render(
    <Harness
      onSubmit={() => {}}
      onEditLatestQueued={onEditLatest}
      onBackgroundSubmit={onBg}
    />,
  );
  const box = screen.getByLabelText('Message');
  await userEvent.type(box, 'launch in bg');
  fireEvent.keyDown(box, { key: 'ArrowUp', altKey: true });
  expect(onEditLatest).toHaveBeenCalledOnce();
  fireEvent.keyDown(box, { key: 'Enter', metaKey: true });
  expect(onBg).toHaveBeenCalledOnce();
});

it('opens / slash command menu and inserts or executes picked commands', async () => {
  const onSlash = vi.fn();
  render(<Harness onSubmit={() => {}} onSlashCommand={onSlash} />);
  const box = screen.getByLabelText('Message');
  const user = userEvent.setup();

  await user.type(box, '/si');
  const menu = await screen.findByRole('listbox', { name: 'Slash commands' });
  expect(menu).toHaveTextContent('/side <question>');
  await user.keyboard('{Enter}');
  expect(box).toHaveValue('/side ');

  await user.clear(box);
  await user.type(box, '/pl');
  await waitFor(() =>
    expect(screen.getByRole('option', { name: /\/plan/ })).toBeVisible(),
  );
  await user.keyboard('{Enter}');
  expect(onSlash).toHaveBeenCalledWith('plan');
  expect(box).toHaveValue('');
});
