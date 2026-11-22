import { mkdir, rm } from 'node:fs/promises';
import tailwind from 'bun-plugin-tailwind';

const outdir = 'dist';
await rm(outdir, { recursive: true, force: true });
await mkdir(outdir);
// Go's embed directive needs at least one file in dist.
await Bun.write(`${outdir}/.gitkeep`, '');

await Bun.build({
  entrypoints: ['index.html'],
  outdir,
  target: 'browser',
  publicPath: '/',
  minify: true,
  splitting: true,
  define: { 'process.env.NODE_ENV': JSON.stringify('production') },
  naming: {
    entry: '[name].[ext]',
    chunk: 'assets/[name]-[hash].[ext]',
    asset: 'assets/[name]-[hash].[ext]',
  },
  plugins: [tailwind],
});
