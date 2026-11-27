import { XLSX_IMPORT_LIMIT, XlsxImportError } from "@/lib/craft-xlsx-import"
import { initializeNativeWorkbook } from "@/lib/mooring/native-document-client"

/** Why the server refused a workbook file before storing anything, in the words of a Sheet. */
export const WORKBOOK_REFUSAL: Record<string, string> = {
  too_large: `This workbook is larger than a Sheet can open (${XLSX_IMPORT_LIMIT >> 20} MB).`,
  unpacked_too_large: "This workbook unpacks to more than a Sheet takes (512 MiB).",
  too_many_parts: "This workbook has more parts than a Sheet takes.",
  not_workbook: "This file is not a readable .xlsx or .xlsm workbook.",
  encrypted: "This workbook is protected with a password, so a Sheet cannot open it.",
  unsupported: "A .xlsb, .xls or .ods workbook cannot be kept as it is in a Sheet.",
  quota: "Storage for Sheets here is full.",
  chat_not_found: "This chat is not yours to add a Sheet to.",
}

/**
 * The step that makes an empty Sheet from a workbook file: the .xlsx or .xlsm goes up as it is and
 * becomes version 1. A file the server refused before storing anything throws an XlsxImportError
 * marked `beforeWrite`, so the creation may try again; any other answer is the creation's to check.
 */
export async function uploadWorkbook(args: { workspaceId: string; pageId: string; file: File; signal: AbortSignal; /** The Sheet belongs to this chat from version 1 (X-Craft-Chat). */ chatId?: string }): Promise<Response> {
  const response = await initializeNativeWorkbook({ workspaceId: args.workspaceId, pageId: args.pageId, file: args.file, signal: args.signal, ...(args.chatId ? { chatId: args.chatId } : {}) })
  if (response.status === 413 || response.status === 415 || response.status === 404) {
    const code = await response.clone().json().then((v: unknown) => v && typeof v === "object" && typeof (v as { code?: unknown }).code === "string" ? (v as { code: string }).code : "", () => "")
    if (/^[a-z][a-z_]{0,63}$/.test(code)) throw Object.assign(new XlsxImportError(code, WORKBOOK_REFUSAL[code] ?? "The server refused this workbook."), { beforeWrite: true })
  }
  return response
}
