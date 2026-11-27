import { useState } from 'react';
import * as DialogPrimitive from '@radix-ui/react-dialog';
import { ChevronLeft, ChevronRight, X } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface LightboxImage {
  src: string;
  alt: string;
}

/**
 * A full-screen view of one image out of a set. Arrow keys and the side
 * buttons step through the set (wrapping around), Escape or a click on the
 * backdrop closes it, and clicking the image toggles between fitting the
 * screen and its real size.
 */
export function Lightbox({
  images,
  index,
  onIndexChange,
  onClose,
}: {
  images: LightboxImage[];
  /** The image shown; null when closed. */
  index: number | null;
  onIndexChange: (i: number) => void;
  onClose: () => void;
}) {
  const [zoomed, setZoomed] = useState(false);
  const open = index !== null && images.length > 0;
  const i = open ? Math.min(index, images.length - 1) : 0;
  const many = images.length > 1;
  const step = (d: number) => {
    setZoomed(false);
    onIndexChange((i + d + images.length) % images.length);
  };
  // The next open starts fitted to the screen.
  const close = () => {
    setZoomed(false);
    onClose();
  };

  const image = images[i];
  return (
    <DialogPrimitive.Root
      open={open}
      onOpenChange={(o) => {
        if (!o) close();
      }}
    >
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/85" />
        <DialogPrimitive.Content
          aria-describedby={undefined}
          className="fixed inset-0 z-50 flex items-center justify-center focus:outline-none"
          onKeyDown={(e) => {
            if (!many) return;
            if (e.key === 'ArrowLeft') {
              e.preventDefault();
              step(-1);
            } else if (e.key === 'ArrowRight') {
              e.preventDefault();
              step(1);
            }
          }}
          onClick={(e) => {
            if (e.target === e.currentTarget) close();
          }}
        >
          <DialogPrimitive.Title className="sr-only">
            {image ? image.alt : 'Image'}
            {many && ` (${i + 1} of ${images.length})`}
          </DialogPrimitive.Title>
          {image && (
            <div
              className={cn(
                'flex max-h-full max-w-full',
                zoomed
                  ? 'overflow-auto'
                  : 'items-center justify-center p-4 md:p-14',
              )}
              onClick={(e) => {
                if (e.target === e.currentTarget) close();
              }}
            >
              <img
                src={image.src}
                alt={image.alt}
                onClick={() => setZoomed((z) => !z)}
                className={cn(
                  'select-none',
                  zoomed
                    ? 'max-w-none cursor-zoom-out'
                    : 'max-h-[calc(100dvh-2rem)] max-w-[calc(100vw-2rem)] cursor-zoom-in rounded-lg object-contain shadow-2xl md:max-h-[calc(100dvh-7rem)] md:max-w-[calc(100vw-7rem)]',
                )}
              />
            </div>
          )}
          {many && (
            <>
              <button
                type="button"
                aria-label="Previous image"
                onClick={() => step(-1)}
                className={navButton('left-3')}
              >
                <ChevronLeft className="size-5" />
              </button>
              <button
                type="button"
                aria-label="Next image"
                onClick={() => step(1)}
                className={navButton('right-3')}
              >
                <ChevronRight className="size-5" />
              </button>
              <span className="absolute bottom-4 left-1/2 -translate-x-1/2 rounded-full bg-black/60 px-3 py-1 text-xs text-white tabular-nums">
                {i + 1} / {images.length}
              </span>
            </>
          )}
          <DialogPrimitive.Close
            aria-label="Close"
            className="absolute top-3 right-3 flex size-10 items-center justify-center rounded-full bg-black/60 text-white transition-colors hover:bg-black/80"
          >
            <X className="size-5" />
          </DialogPrimitive.Close>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

const navButton = (side: string) =>
  cn(
    'absolute top-1/2 flex size-10 -translate-y-1/2 items-center justify-center rounded-full bg-black/60 text-white transition-colors hover:bg-black/80',
    side,
  );
