// Preload: serves local font files to OpenPencil's headless renderer.
// SF Pro comes from ~/Library/Fonts (Apple license, never copied here); OFL fonts live in design/fonts/.
import { readFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { fileURLToPath } from 'node:url';
import { fontManager } from '@open-pencil/core';

const OFL = fileURLToPath(new URL('../fonts/', import.meta.url));
const buf = (b) => b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength);
fontManager.setHostFontLoader(async (family, style) => {
  const s = (style || 'Regular').replace(/\s+/g, '');
  const sf = /^SF Pro (Display|Text)$/.exec(family);
  const path = sf
    ? `${homedir()}/Library/Fonts/SF-Pro-${sf[1]}-${s.replace(/^SemiBold$/i, 'Semibold')}.otf`
    : `${OFL}${family.replace(/\s+/g, '')}-${s}.ttf`;
  try { return buf(await readFile(path)); } catch { return null; }
});
