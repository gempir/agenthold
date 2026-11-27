import { useEffect } from 'react';

export const APP_TITLE = 'agenthold';

/** The tab title for a page: its parts joined with " · ", most specific
 * first, or just the app name when there are none. Empty parts are skipped. */
export function documentTitle(...parts: (string | null | undefined)[]) {
  const shown = parts.map((p) => p?.trim()).filter(Boolean);
  return shown.length ? shown.join(' · ') : APP_TITLE;
}

/** Sets the browser tab's title while the calling page is shown, and puts
 * the app name back when it goes away. */
export function useDocumentTitle(...parts: (string | null | undefined)[]) {
  const title = documentTitle(...parts);
  useEffect(() => {
    document.title = title;
  }, [title]);
  useEffect(
    () => () => {
      document.title = APP_TITLE;
    },
    [],
  );
}
