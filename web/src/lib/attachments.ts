import { useCallback, useEffect, useRef, useState } from 'react';
import { contentUrl } from '@/lib/files';

export const IMAGE_TYPES = [
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
] as const;
export type ImageType = (typeof IMAGE_TYPES)[number];
export const MAX_IMAGES = 4;
export const MAX_IMAGE_BYTES = 5 * 1024 * 1024;
export const MAX_FILES = 10;
export const MAX_FILE_BYTES = 20 * 1024 * 1024;
/** Pastes at or above 32 KiB automatically turn into a virtual text file chip. */
export const LARGE_PASTE_BYTES = 32 * 1024;

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

/** A non-image file waiting to be uploaded to the agent workspace on send. */
export interface FileAttachment {
  id: string;
  name: string;
  mimeType: string;
  size: number;
  file: File;
  /** Workspace-relative path once uploaded (e.g. .agenthold/attachments/...). */
  uploadedPath?: string;
  /** Set when created from a >= 32 KiB paste so the user can inline it if desired. */
  fromLargePaste?: boolean;
  /** Original pasted text for large pastes (allows converting back to inline text). */
  pasteText?: string;
}

/** The API's wire form of an image attachment. */
export function toImagePayload(list: Attachment[]) {
  return list.map(({ mimeType, data }) => ({ mimeType, data }));
}

export function isImageType(t: string): t is ImageType {
  return (IMAGE_TYPES as readonly string[]).includes(t);
}

export function utf8ByteLength(text: string): number {
  return new TextEncoder().encode(text).length;
}

export function createLargePasteFile(text: string): File {
  const stamp = new Date()
    .toISOString()
    .replace(/[-:T.Z]/g, '')
    .slice(0, 14);
  return new File([text], `paste-${stamp}.txt`, { type: 'text/plain' });
}

function sanitizeAttachmentName(name: string): string {
  const base = name.split(/[/\\]/).pop()?.trim() || 'attachment';
  const cleaned = base.replace(/[^a-zA-Z0-9._-]/g, '_');
  return cleaned || 'attachment';
}

export interface UploadedAttachment {
  id: string;
  name: string;
  path: string;
  size: number;
}

/**
 * Uploads non-image file attachments to `.agenthold/attachments/<name>` inside
 * the agent's workspace so the agent can read them directly.
 */
export async function uploadFileAttachments(
  agentId: string,
  files: FileAttachment[],
): Promise<UploadedAttachment[]> {
  const results: UploadedAttachment[] = [];
  for (const item of files) {
    if (item.uploadedPath) {
      results.push({
        id: item.id,
        name: item.name,
        path: item.uploadedPath,
        size: item.size,
      });
      continue;
    }
    const safeName = sanitizeAttachmentName(item.name);
    const shortId = item.id.slice(0, 8);
    const path = `.agenthold/attachments/${shortId}-${safeName}`;
    const res = await fetch(contentUrl(agentId, path), {
      method: 'PUT',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/octet-stream' },
      body: item.file,
    });
    if (!res.ok) {
      let msg = `Could not upload ${item.name} (${res.status})`;
      try {
        const j = (await res.json()) as { error?: string };
        if (j.error) msg = j.error;
      } catch {
        // non-JSON error body
      }
      throw new Error(msg);
    }
    results.push({ id: item.id, name: item.name, path, size: item.size });
  }
  return results;
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
 * Images and general files attached to the message being written. Images stay
 * inline as base64 (`items`); any other file (PDF, text, JSON, code, logs,
 * large paste) is held in `files` to be uploaded to the agent workspace.
 */
export function useAttachments() {
  const [items, setItems] = useState<Attachment[]>([]);
  const [files, setFiles] = useState<FileAttachment[]>([]);
  const [error, setError] = useState<string | null>(null);
  const liveImages = useRef<Attachment[]>([]);
  const liveFiles = useRef<FileAttachment[]>([]);
  useEffect(() => {
    liveImages.current = items;
  }, [items]);
  useEffect(() => {
    liveFiles.current = files;
  }, [files]);

  const add = useCallback(async (incoming: File[]) => {
    if (incoming.length === 0) return;
    const problems: string[] = [];
    const imageFiles: File[] = [];
    const otherFiles: File[] = [];

    for (const f of incoming) {
      if (isImageType(f.type)) imageFiles.push(f);
      else otherFiles.push(f);
    }

    const imageRoom = MAX_IMAGES - liveImages.current.length;
    const validImages = imageFiles.filter((f) => {
      if (f.size <= MAX_IMAGE_BYTES) return true;
      problems.push(`${f.name || 'Image'} is over 5 MB`);
      return false;
    });
    if (validImages.length > imageRoom)
      problems.push(`At most ${MAX_IMAGES} images`);

    const addedImages: Attachment[] = [];
    for (const f of validImages.slice(0, Math.max(imageRoom, 0))) {
      try {
        const data = await readBase64(f);
        addedImages.push({
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

    const fileRoom = MAX_FILES - liveFiles.current.length;
    const validOthers = otherFiles.filter((f) => {
      if (f.size <= MAX_FILE_BYTES) return true;
      problems.push(`${f.name || 'File'} is over 20 MB`);
      return false;
    });
    if (validOthers.length > fileRoom)
      problems.push(`At most ${MAX_FILES} files`);

    const addedFiles: FileAttachment[] = validOthers
      .slice(0, Math.max(fileRoom, 0))
      .map((f) => ({
        id: crypto.randomUUID(),
        name: f.name || 'attachment',
        mimeType: f.type || 'application/octet-stream',
        size: f.size,
        file: f,
      }));

    setError(problems.length ? problems.join('. ') : null);
    if (addedImages.length)
      setItems((cur) => [...cur, ...addedImages].slice(0, MAX_IMAGES));
    if (addedFiles.length)
      setFiles((cur) => [...cur, ...addedFiles].slice(0, MAX_FILES));
  }, []);

  const addLargePaste = useCallback((text: string): FileAttachment | null => {
    const fileRoom = MAX_FILES - liveFiles.current.length;
    if (fileRoom <= 0) {
      setError(`At most ${MAX_FILES} files`);
      return null;
    }
    const f = createLargePasteFile(text);
    const entry: FileAttachment = {
      id: crypto.randomUUID(),
      name: f.name,
      mimeType: 'text/plain',
      size: f.size,
      file: f,
      fromLargePaste: true,
      pasteText: text,
    };
    setError(null);
    setFiles((cur) => [...cur, entry].slice(0, MAX_FILES));
    return entry;
  }, []);

  const remove = useCallback((id: string) => {
    setError(null);
    setItems((cur) => cur.filter((a) => a.id !== id));
  }, []);

  const removeFile = useCallback((id: string) => {
    setError(null);
    setFiles((cur) => cur.filter((a) => a.id !== id));
  }, []);

  const clear = useCallback(() => {
    setItems([]);
    setFiles([]);
    setError(null);
  }, []);

  return {
    items,
    files,
    error,
    add,
    addLargePaste,
    remove,
    removeFile,
    clear,
  };
}
