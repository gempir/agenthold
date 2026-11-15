export type Theme = 'dark' | 'light';

const KEY = 'agenthold.theme';

export function getTheme(): Theme {
  return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark';
}

/** Applies the theme to <html> and remembers it; dark is the default. */
export function setTheme(t: Theme) {
  if (t === 'light') document.documentElement.dataset.theme = 'light';
  else delete document.documentElement.dataset.theme;
  try {
    if (t === 'light') localStorage.setItem(KEY, t);
    else localStorage.removeItem(KEY);
  } catch {
    // storage unavailable: the choice lasts for this page only
  }
}
