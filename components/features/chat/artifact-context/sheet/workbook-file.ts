/** Workbooks a Sheet keeps as the file they are: .xlsx and macro-enabled .xlsm (its VBA kept, never run). */
export const WORKBOOK_EXTENSIONS = [".xlsx", ".xlsm"] as const
export const WORKBOOK_TYPES = ["application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "application/vnd.ms-excel.sheet.macroEnabled.12"] as const
/** For a file input's accept: both formats, by extension and by type. */
export const WORKBOOK_ACCEPT = [...WORKBOOK_EXTENSIONS, ...WORKBOOK_TYPES].join(",")
export const isWorkbookName = (name: string) => /^.+\.(xlsx|xlsm)$/i.test(name)
export const workbookBaseName = (name: string) => name.replace(/\.(xlsx|xlsm)$/i, "").trim() || "Sheet"
