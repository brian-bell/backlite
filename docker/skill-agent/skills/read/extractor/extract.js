#!/usr/bin/env node
// extract.js — read HTML from arg1, write markdown to arg2.
// Pipeline: jsdom → @mozilla/readability → turndown.
// Exits non-zero if the extraction yields no usable content; the caller
// (fetch-and-extract.sh) tolerates that and falls back to raw-only.

'use strict';

const fs = require('fs');
const { JSDOM } = require('jsdom');
const { Readability } = require('@mozilla/readability');
const TurndownService = require('turndown');

if (process.argv.length < 4) {
  console.error('usage: extract.js <input.html> <output.md>');
  process.exit(2);
}

const inputPath = process.argv[2];
const outputPath = process.argv[3];

const html = fs.readFileSync(inputPath, 'utf8');
const dom = new JSDOM(html, { url: 'about:blank' });
const article = new Readability(dom.window.document).parse();

if (!article || !article.content) {
  console.error('extract.js: Readability produced no content');
  process.exit(1);
}

const turndown = new TurndownService({
  headingStyle: 'atx',
  codeBlockStyle: 'fenced',
});
let markdown = turndown.turndown(article.content);
if (article.title) {
  markdown = `# ${article.title}\n\n${markdown}`;
}

fs.writeFileSync(outputPath, markdown, 'utf8');
