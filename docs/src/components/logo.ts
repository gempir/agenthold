/**
 * The site's logo, drawn once for every place it appears: the header and the
 * docs layout (Logo.astro) and the social cards (src/pages/og/_card.ts).
 * Three stacked bars, the same mark as the app's sidebar.
 */
export const logo = {
  width: 32,
  height: 32,
  paths: [
    "M5 3h22a3 3 0 0 1 0 6H5a3 3 0 0 1 0-6Z",
    "M5 13h22a3 3 0 0 1 0 6H5a3 3 0 0 1 0-6Z",
    "M5 23h22a3 3 0 0 1 0 6H5a3 3 0 0 1 0-6Z",
  ],
};
