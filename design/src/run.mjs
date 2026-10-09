// usage: node --import ./src/fonts.mjs src/run.mjs <build.mjs> <out.fig> [in.fig]
// Like `openpencil eval -o`, but with a real CanvasKit text measurer + layout pass before writing.
import { readFile, writeFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import { IORegistry, BUILTIN_IO_FORMATS } from '@open-pencil/core/io';
import { computeAllLayouts } from '@open-pencil/core/layout';
import { FigmaAPI, SkiaRenderer, initCanvasKit } from '@open-pencil/core';
import { SceneGraph } from '@open-pencil/scene-graph';

const [buildPath, outPath, inPath = 'NEW'] = process.argv.slice(2);
const io = new IORegistry(BUILTIN_IO_FORMATS);
const graph = inPath === 'NEW' ? new SceneGraph()
  : (await io.readDocument({ name: inPath, data: new Uint8Array(await readFile(inPath)) })).graph;

const ck = await initCanvasKit();
const renderer = new SkiaRenderer(ck, ck.MakeSurface(1, 1));
await renderer.loadFonts();

const figma = new FigmaAPI(graph);
const build = (await import(pathToFileURL(buildPath).href)).default;
const result = await build(figma);

for (const page of graph.getPages ? graph.getPages() : []) {
  await renderer.prepareForExport(graph, page.id, page.childIds ?? []); // sets measurer + computeAllLayouts
}
computeAllLayouts(graph);
await writeFile(outPath, (await io.writeDocument('fig', graph)).data);
console.log(JSON.stringify(result ?? 'ok'));
