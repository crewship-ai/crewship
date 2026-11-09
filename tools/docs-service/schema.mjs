import { Extension, Node, Mark, getSchema } from '@tiptap/core';
import { Table, TableRow, TableCell, TableHeader } from '@tiptap/extension-table';
import { Plugin } from '@tiptap/pm/state';
import * as Y from 'yjs';
import { prosemirrorToYDoc, yDocToProsemirrorJSON } from '@tiptap/y-tiptap';

import { createDocumentSchema } from './schema-core.mjs';
export { CREATE_VERSION, CAPABILITY_ORDER, SCHEMA_CAPABILITIES, schemaAllows } from './schema-core.mjs';
export const { VERSION, RICH_VERSION, FORMATTED_VERSION, MARKS_VERSION, CANVAS_VERSION, LATEST_VERSION, PALETTE, validColor, ATOMS, validDate, validUserId, UNKNOWN_MEMBER, validLanguage, extensionsFor, extensions, schema, schemaFor, validLinkHref, encode, decode, canonical, hash, load, snapshot, project, validateJSON, validate, outline, textOf, find, textNode, patch, anchor, resolve, initialJSON, create, markdown } = createDocumentSchema({ Extension, Node, Mark, getSchema, Table, TableRow, TableCell, TableHeader, Plugin, Y, prosemirrorToYDoc, yDocToProsemirrorJSON });
