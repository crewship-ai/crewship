import { createPrivateArtifacts } from './helpers/private-artifacts.mjs';
// Explicit Dev2-only fixture upload; synthetic QA documents, not agent-generated output.
import { chromium } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';
const directory=`preview-demo-${new Date().toISOString().slice(0,10)}-${crypto.randomUUID().slice(0,8)}`;
const local=await createPrivateArtifacts('files-preview-demo');
const browser=await chromium.launch({headless:true});
try {
 const page=await browser.newPage({viewport:{width:900,height:500}});
 await page.setContent(`<style>@page{size:A4;margin:22mm}body{font:16px Arial;color:#16324c}section{break-after:page}h1{font-size:32px}small{color:#5a6b7c}</style><section><small>CREWSHIP / SYNTHETIC QA FIXTURE</small><h1>Files preview demo</h1><p>Page 1 of 2. This document verifies native PDF preview, page navigation and downloading.</p><p>Prepared for Mařena's Files panel. This is sample data uploaded through the supported Crewship CLI; it is not an agent's work product.</p><hr><p>Acceptance: readable content, correctly rendered pages, working Next page control.</p></section><section style="break-after:auto"><small>CREWSHIP / SYNTHETIC QA FIXTURE</small><h1>Preview verified</h1><p>Page 2 of 2. This deliberately different second page verifies PDF navigation.</p><p>Demo milestone: design review, implementation, verification.</p></section>`);
 await page.pdf({path:path.join(local,'demo-preview.pdf'),printBackground:true});
 await page.setContent(`<style>body{margin:0;background:#101b2a;color:white;font:24px Arial}main{padding:60px}.label{color:#7dd3fc;font-size:16px}h1{font-size:42px}.cards{display:flex;gap:16px}.cards div{background:#263950;padding:28px;border-radius:14px}</style><main><div class="label">CREWSHIP · SYNTHETIC QA FIXTURE</div><h1>Image preview demo</h1><div class="cards"><div>Design</div><div>Build</div><div>Verify</div></div><p>Uploaded sample image — not agent-generated output.</p></main>`);
 await page.screenshot({path:path.join(local,'demo-preview.png')});
 await fs.writeFile(path.join(local,'demo-preview.ts'),'// Synthetic Crewship Files preview fixture; not agent-generated output.\nexport const previewDemo = {\n  title: "Files preview",\n  checks: ["PDF pages", "image rendering", "code preview", "download"],\n} as const\n');
 const files=['demo-preview.pdf','demo-preview.png','demo-preview.ts'];
 for(const name of files) execFileSync('/tmp/crewship-2-dev',['--profile','dev2','--server','http://localhost:8082','agent','file-write','ma-ena',directory+'/'+name,'--from',path.join(local,name)],{stdio:['ignore','pipe','pipe']});
 const crew_file=`shared/${directory}/crew-demo-preview.pdf`;
 execFileSync('/tmp/crewship-2-dev',['--profile','dev2','--server','http://localhost:8082','crew','files','save','copy-site',crew_file,'--file',path.join(local,'demo-preview.pdf')],{stdio:['ignore','pipe','pipe']});
 const manifest={crew_file,server:'https://crewship-dev2.unifylab.cz',agent_slug:'ma-ena',directory,local,files,description:'Synthetic QA fixtures uploaded with owner CLI; no agent model execution.'};
 const manifestPath = path.join(local, 'files-preview-demo-manifest.json');
 await fs.writeFile(manifestPath,JSON.stringify(manifest,null,2),{mode:0o600,flag:'wx'});
 console.log(`FILES_PREVIEW_MANIFEST=${manifestPath}`);
 console.log(JSON.stringify(manifest,null,2));
} finally {await browser.close();}
