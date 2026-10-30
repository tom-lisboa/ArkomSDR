import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import { compile } from 'svelte/compiler';
import { operations, curlExample } from '../src/lib/api.js';
await import('./sync-docs.mjs');
const source = JSON.parse(await readFile('../internal/management/openapi.json','utf8'));
const published = JSON.parse(await readFile('public/openapi.json','utf8'));
assert.deepEqual(published,source,'Public contract must match the Go contract');
const data = JSON.parse(await readFile('src/lib/content.generated.json','utf8'));
const ops = operations(source);
assert.equal(ops.length,27);
for (const op of ops) {
  const example=curlExample(op,source.servers[0].url);
  assert(!example.includes('undefined'));
  if (op.security?.length===0) assert(!example.includes('Bearer'));
  else assert(example.includes('Bearer SEU_ACCESS_TOKEN'));
  if (op.parameters?.some(p=>p.name==='Idempotency-Key')) assert(example.includes("--header 'Idempotency-Key:"));
}
const publicText=JSON.stringify(data)+await readFile('public/guia-integracao.md','utf8');
for (const forbidden of ['management-admin.env','/etc/arkom-bia','/Users/tomlisboa','.ssh/','ARKOM_MANAGEMENT_PASSWORD','TWILIO_AUTH_TOKEN']) assert(!publicText.includes(forbidden),`Operational information leaked: ${forbidden}`);
for (const group of data.groups) for (const id of group.items) assert(['inicio','api'].includes(id)||data.articles.some(a=>a.id===id),`Broken navigation: ${id}`);
let components=0;
for (const directory of ['src','src/lib']) for (const filename of await readdir(directory)) if (filename.endsWith('.svelte')) {
 const {warnings}=compile(await readFile(`${directory}/${filename}`,'utf8'),{filename:`${directory}/${filename}`,generate:'client'});
 assert.deepEqual(warnings.map(w=>`${w.code}: ${w.message}`),[],`Svelte warnings in ${filename}`);components++;
}
console.log(`Verified ${ops.length} operations, ${data.articles.length} guides, ${components} Svelte components; no operational credentials in public content.`);
