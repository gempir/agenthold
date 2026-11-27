import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { expect, it, vi } from 'bun:test';
import { useState } from 'react';
import { Composer } from './Composer';
import { toImagePayload, useAttachments } from '@/lib/attachments';

const png = () =>
  new File([new Uint8Array([137, 80, 78, 71])], 'shot.png', {
    type: 'image/png',
  });

function Harness({ onSubmit }: { onSubmit: (images: unknown) => void }) {
  const [text, setText] = useState('');
  const att = useAttachments();
  return (
    <Composer
      value={text}
      onChange={setText}
      placeholder="Message"
      attachments={att.items}
      attachError={att.error}
      onAttach={att.add}
      onRemoveAttachment={att.remove}
      onSubmit={() => onSubmit(toImagePayload(att.items))}
    />
  );
}

it('attaches a picked image, sends it without text, and removes it', async () => {
  const onSubmit = vi.fn();
  render(<Harness onSubmit={onSubmit} />);
  expect((screen.getByLabelText('Send') as HTMLButtonElement).disabled).toBe(
    true,
  );

  fireEvent.change(screen.getByLabelText('Attach images'), {
    target: { files: [png()] },
  });
  const thumb = await screen.findByAltText('shot.png');
  // A data: URL: the CSP's img-src does not allow blob: URLs.
  expect(thumb.getAttribute('src')).toBe('data:image/png;base64,iVBORw==');
  fireEvent.click(screen.getByLabelText('Send'));
  expect(onSubmit).toHaveBeenCalledWith([
    { mimeType: 'image/png', data: 'iVBORw==' },
  ]);

  fireEvent.click(screen.getByLabelText('View shot.png'));
  expect(screen.getByRole('dialog')).toBeTruthy();
  fireEvent.click(screen.getByLabelText('Close'));

  fireEvent.click(screen.getByLabelText('Remove shot.png'));
  expect(screen.queryByAltText('shot.png')).toBeNull();
});

it('takes an image pasted into the box and refuses other files', async () => {
  render(<Harness onSubmit={() => {}} />);
  const box = screen.getByLabelText('Message');
  fireEvent.paste(box, { clipboardData: { files: [png()] } });
  await screen.findByAltText('shot.png');

  const pdf = new File(['x'], 'doc.pdf', { type: 'application/pdf' });
  fireEvent.change(screen.getByLabelText('Attach images'), {
    target: { files: [pdf] },
  });
  await waitFor(() =>
    expect(screen.getByRole('alert').textContent).toContain('doc.pdf'),
  );
});
