import { useCallback, useEffect, useRef, useState } from 'react';

export const IMAGE_TYPES = [
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
] as const;
export type ImageType = (typeof IMAGE_TYPES)[number];
export const MAX_IMAGES = 4;
export const MAX_IMAGE_BYTES = 5 * 1024 * 1024;

/** An image waiting to be sent with a message. */
export interface Attachment {
  id: string;
  name: string;
  mimeType: ImageType;
  /** Base64 without a data: prefix, as the API wants it. */
  data: string;
  /** data: URL for the thumbnail. Not a blob: URL, which the CSP's img-src
   * does not allow. */
  previewUrl: string;
}

/** The API's wire form of an attachment. */
export function toImagePayload(list: Attachment[]) {
  return list.map(({ mimeType, data }) => ({ mimeType, data }));
}

export function isImageType(t: string): t is ImageType {
  return (IMAGE_TYPES as readonly string[]).includes(t);
}

function readBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onerror = () => reject(r.error ?? new Error('Could not read the file'));
    r.onload = () => {
      const s = String(r.result);
      resolve(s.slice(s.indexOf(',') + 1));
    };
    r.readAsDataURL(file);
  });
}

/**
 * The images attached to the message being written. `add` takes files from a
 * picker or the clipboard, skips what is not a supported image, and reports
 * what it refused in `error`.
 */
export function useAttachments() {
  const [items, setItems] = useState<Attachment[]>([]);
  const [error, setError] = useState<string | null>(null);
  const live = useRef<Attachment[]>([]);
  useEffect(() => {
    live.current = items;
  }, [items]);

  const add = useCallback(async (files: File[]) => {
    const room = MAX_IMAGES - live.current.length;
    const problems: string[] = [];
    const images = files.filter((f) => {
      if (isImageType(f.type)) return true;
      problems.push(
        `${f.name || 'File'} is not a PNG, JPEG, GIF or WebP image`,
      );
      return false;
    });
    const fits = images.filter((f) => {
      if (f.size <= MAX_IMAGE_BYTES) return true;
      problems.push(`${f.name || 'Image'} is over 5 MB`);
      return false;
    });
    if (fits.length > room) problems.push(`At most ${MAX_IMAGES} images`);
    const added: Attachment[] = [];
    for (const f of fits.slice(0, Math.max(room, 0))) {
      try {
        const data = await readBase64(f);
        added.push({
          id: crypto.randomUUID(),
          name: f.name || 'pasted image',
          mimeType: f.type as ImageType,
          data,
          previewUrl: `data:${f.type};base64,${data}`,
        });
      } catch {
        problems.push(`Could not read ${f.name || 'the image'}`);
      }
    }
    setError(problems.length ? problems.join('. ') : null);
    if (added.length)
      setItems((cur) => [...cur, ...added].slice(0, MAX_IMAGES));
  }, []);

  const remove = useCallback((id: string) => {
    setError(null);
    setItems((cur) => cur.filter((a) => a.id !== id));
  }, []);

  const clear = useCallback(() => {
    setItems([]);
    setError(null);
  }, []);

  return { items, error, add, remove, clear };
}
