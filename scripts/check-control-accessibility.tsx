import React from "react"
import { renderToStaticMarkup } from "react-dom/server"
import fs from "node:fs/promises"
import postcss from "postcss"
import tailwind from "@tailwindcss/postcss"
import { chromium } from "playwright"
import { Input } from "../components/ui/input"
import { Textarea } from "../components/ui/textarea"
import { Button } from "../components/ui/button"
import { Select, SelectTrigger, SelectValue } from "../components/ui/select"
import { Spinner } from "../components/ui/spinner"
import { SidebarFilterButton } from "../components/layout/sidebar-kit"

const css = (await postcss([tailwind()]).process(await fs.readFile("app/globals.css", "utf8"), {from:"app/globals.css"})).css
const markup=renderToStaticMarkup(<section className="bg-card p-6 flex flex-col gap-4 max-w-md"><h1>Pages controls</h1><Input aria-label="Name" placeholder="Page name"/><Textarea aria-label="Description" placeholder="Description"/><Select><SelectTrigger><SelectValue placeholder="Choose a crew"/></SelectTrigger></Select><Button variant="outline">Review changes</Button><SidebarFilterButton/><div className="flex gap-2"><Spinner/><span>Reading source…</span></div></section>)
const browser=await chromium.launch({headless:true})
try {
 const page=await browser.newPage({viewport:{width:640,height:600},reducedMotion:"reduce"})
 const results=[]
 for(const theme of ["dark", "light"]){
  await page.setContent(`<html class="${theme==='dark'?'dark':''}"><head><style>${css}</style></head><body class="bg-background text-foreground p-8">${markup}</body></html>`)
  await page.evaluate("globalThis.__name = (target) => target")
  const data=await page.evaluate(()=>{
   const ctx=document.createElement('canvas').getContext('2d')!
   const color=(s:string)=>{ctx.clearRect(0,0,1,1);ctx.fillStyle=s;ctx.fillRect(0,0,1,1);return Array.from(ctx.getImageData(0,0,1,1).data)}
   const mix=(a:number[],b:number[])=>[0,1,2].map(i=>a[i]*a[3]/255+b[i]*(1-a[3]/255)).concat(255)
   const bg=(el:Element|null):number[]=>el?mix(color(getComputedStyle(el).backgroundColor),bg(el.parentElement)):[255,255,255,255]
   const lum=(c:number[])=>c.slice(0,3).map(v=>{v/=255;return v<=0.04045?v/12.92:((v+0.055)/1.055)**2.4}).reduce((a,v,i)=>a+v*[.2126,.7152,.0722][i],0)
   const ratio=(a:number[],b:number[])=>{const x=lum(a),y=lum(b);return (Math.max(x,y)+.05)/(Math.min(x,y)+.05)}
   const controls=Array.from(document.querySelectorAll('[data-slot="input"],[data-slot="textarea"],[data-slot="select-trigger"],[data-variant="outline"]')).map(el=>({slot:el.getAttribute('data-slot'),contrast:Math.min(ratio(color(getComputedStyle(el).borderTopColor),bg(el)),ratio(color(getComputedStyle(el).borderTopColor),bg(el.parentElement)))}))
   const filter=Array.from(document.querySelectorAll('button')).find(el=>el.textContent==='Filter')!
   const spinner=document.querySelector('.animate-spin')!
   return {controls,filterTextContrast:ratio(color(getComputedStyle(filter).color),bg(filter)),spinnerAnimation:getComputedStyle(spinner).animationName}
  })
  results.push({theme,...data})
  if(theme==='dark') await page.screenshot({path:'/tmp/pages-completion-controls.png'})
 }
 await fs.writeFile('/tmp/pages-completion-controls.json',JSON.stringify(results,null,2))
 console.log(JSON.stringify(results,null,2))
 if(results.some(r=>r.controls.some(c=>c.contrast<3)||r.filterTextContrast<4.5||r.spinnerAnimation!=="none")) process.exitCode=1
} finally {await browser.close()}
