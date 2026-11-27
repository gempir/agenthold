import { fireEvent, render, screen } from '@testing-library/react';
import { expect, it } from 'bun:test';
import { useState } from 'react';
import { Lightbox } from './Lightbox';

const images = ['a', 'b', 'c'].map((n) => ({
  src: `data:image/png;base64,${n}`,
  alt: `img ${n}`,
}));

function Harness({ list = images }: { list?: typeof images }) {
  const [i, setI] = useState<number | null>(null);
  return (
    <>
      <button onClick={() => setI(0)}>open</button>
      <Lightbox
        images={list}
        index={i}
        onIndexChange={setI}
        onClose={() => setI(null)}
      />
    </>
  );
}

const shown = () =>
  screen.getByRole('dialog').querySelector('img')!.getAttribute('alt');

it('steps through images with buttons and arrow keys, wrapping around', () => {
  render(<Harness />);
  fireEvent.click(screen.getByText('open'));
  expect(shown()).toBe('img a');
  expect(screen.getByText('1 / 3')).toBeTruthy();

  fireEvent.click(screen.getByLabelText('Next image'));
  expect(shown()).toBe('img b');
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'ArrowRight' });
  expect(shown()).toBe('img c');
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'ArrowRight' });
  expect(shown()).toBe('img a');
  fireEvent.click(screen.getByLabelText('Previous image'));
  expect(shown()).toBe('img c');

  fireEvent.click(screen.getByLabelText('Close'));
  expect(screen.queryByRole('dialog')).toBeNull();
});

it('shows no navigation for a single image and closes on Escape', () => {
  render(<Harness list={images.slice(0, 1)} />);
  fireEvent.click(screen.getByText('open'));
  expect(screen.queryByLabelText('Next image')).toBeNull();
  expect(screen.queryByText('1 / 1')).toBeNull();
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
  expect(screen.queryByRole('dialog')).toBeNull();
});
