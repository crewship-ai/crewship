import { DOMParser } from '@xmldom/xmldom';
import { check } from './model.mjs';
export const drawingNS = 'http://schemas.openxmlformats.org/drawingml/2006/main';
export const presentationNS = 'http://schemas.openxmlformats.org/presentationml/2006/main';
export function parseXML(source) {
  check(!/<!DOCTYPE/i.test(source), 'DOCTYPE not admitted in generated OOXML');
  return new DOMParser().parseFromString(source, 'application/xml');
}
export const descendants = (node, ns, name) => Array.from(node.getElementsByTagNameNS(ns, name));
export function drawingText(body) {
  function walk(node) {
    if (node.namespaceURI === drawingNS && node.localName === 't') return node.textContent;
    if (node.namespaceURI === drawingNS && node.localName === 'br') return '\n';
    return Array.from(node.childNodes ?? []).map(walk).join('');
  }
  return Array.from(body.childNodes).filter(n => n.namespaceURI === drawingNS && n.localName === 'p').map(walk).join('\n');
}
export function textShape(document, name) {
  const shape = descendants(document, presentationNS, 'sp').find(node => descendants(node, presentationNS, 'cNvPr')[0]?.getAttribute('name') === name);
  check(shape, `native text shape ${name}`);
  const body = descendants(shape, presentationNS, 'txBody')[0]; check(body, `text body ${name}`); return drawingText(body);
}

export function tableShape(document, name) {
  const frame = descendants(document, presentationNS, 'graphicFrame').find(node => descendants(node, presentationNS, 'cNvPr')[0]?.getAttribute('name') === name);
  check(frame, `native table frame ${name}`);
  const tables = descendants(frame, drawingNS, 'tbl'); check(tables.length === 1, `one table per native frame ${name}`);
  return descendants(tables[0], drawingNS, 'tr').map(row => descendants(row, drawingNS, 'tc').map(cell => drawingText(descendants(cell, drawingNS, 'txBody')[0])));
}
