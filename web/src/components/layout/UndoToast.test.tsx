import { render, screen, fireEvent } from '@testing-library/react';
import { expect, it, mock } from 'bun:test';
import { UndoToast } from './UndoToast';

it('undoes by button and keyboard, while preserving editor undo', () => {
  const undo = mock(() => {});
  const close = mock(() => {});
  render(
    <UndoToast action={{ label: 'Thread pinned', undo }} onClose={close} />,
  );
  fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
  expect(undo).toHaveBeenCalledTimes(1);
  fireEvent.keyDown(window, { key: 'z', ctrlKey: true });
  expect(undo).toHaveBeenCalledTimes(2);
  const input = document.createElement('input');
  document.body.append(input);
  fireEvent.keyDown(input, { key: 'z', ctrlKey: true, bubbles: true });
  expect(undo).toHaveBeenCalledTimes(2);
  input.remove();
});
