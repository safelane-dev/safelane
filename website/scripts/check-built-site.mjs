import { readFile, readdir } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const dist = new URL("../dist/", import.meta.url);
const docs = new URL("../src/content/docs/", import.meta.url);
const canonicalOrigin = "https://andrewmaged814.github.io";
const canonicalBase = "/safelane";
const baseHome = `${canonicalBase}/`;

async function findHtmlFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = await Promise.all(entries.map(async (entry) => {
    const path = join(directory, entry.name);
    return entry.isDirectory() ? findHtmlFiles(path) : path.endsWith(".html") ? [path] : [];
  }));
  return files.flat();
}

function decodeHtml(value) {
  return value
    .replaceAll("&quot;", '"')
    .replaceAll("&#39;", "'")
    .replaceAll("&lt;", "<")
    .replaceAll("&gt;", ">")
    .replaceAll("&amp;", "&");
}

const htmlFiles = await findHtmlFiles(fileURLToPath(dist));
let diagramCount = 0;
let siteTitleCount = 0;

for (const file of htmlFiles) {
  const html = await readFile(file, "utf8");

  if (html.includes("qualityteam.me")) {
    throw new Error(`${file} contains the retired qualityteam.me domain`);
  }

  const siteTitle = html.match(/<a href="([^"]+)" class="site-title\b/);
  if (siteTitle) {
    siteTitleCount += 1;
    if (siteTitle[1] !== baseHome) {
      throw new Error(`${file} has an invalid site title link: ${siteTitle[1]}`);
    }
  }

  for (const match of html.matchAll(/<pre\b(?=[^>]*\bclass="mermaid")[^>]*>([\s\S]*?)<\/pre>/g)) {
    const source = decodeHtml(match[1]);
    if (/[“”]/.test(source) || source.includes("—>")) {
      throw new Error(`${file} contains typographic characters that corrupt Mermaid syntax`);
    }
    if (!/^\s*(flowchart|graph|sequenceDiagram|classDiagram|stateDiagram|erDiagram|journey|gantt|pie|mindmap|timeline|quadrantChart|requirementDiagram|gitGraph|C4\w*)\b/.test(source)) {
      throw new Error(`${file} contains an unrecognized Mermaid diagram declaration`);
    }
    diagramCount += 1;
  }
}

const index = await readFile(new URL("index.html", dist), "utf8");
if (!index.includes(`<link rel="canonical" href="${canonicalOrigin}${canonicalBase}"`)) {
  throw new Error("The generated homepage does not use the canonical GitHub Pages URL");
}

if (siteTitleCount !== htmlFiles.length) {
  throw new Error(`Expected a site title link in ${htmlFiles.length} pages, found ${siteTitleCount}`);
}

const markdownFiles = await (async function findMarkdownFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = await Promise.all(entries.map(async (entry) => {
    const path = join(directory, entry.name);
    return entry.isDirectory()
      ? findMarkdownFiles(path)
      : /\.mdx?$/.test(path) ? [path] : [];
  }));
  return files.flat();
})(fileURLToPath(docs));
let sourceDiagramCount = 0;
for (const file of markdownFiles) {
  sourceDiagramCount += (await readFile(file, "utf8")).match(/^```mermaid\s*$/gm)?.length ?? 0;
}

if (diagramCount !== sourceDiagramCount || diagramCount === 0) {
  throw new Error(`Expected ${sourceDiagramCount} built Mermaid diagrams, found ${diagramCount}`);
}

console.log(`Validated ${htmlFiles.length} HTML files and ${diagramCount} Mermaid diagrams.`);
