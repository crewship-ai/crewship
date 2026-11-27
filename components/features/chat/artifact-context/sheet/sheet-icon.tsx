import { FileSpreadsheet } from "lucide-react"
import { cn } from "@/lib/utils"

/** The Sheet's mark: the Excel file type's green, from the --sheet token, on its soft tile. */
export function SheetIcon({ className, size = "md" }: { className?: string; size?: "sm" | "md" }) {
  return <span aria-hidden="true" className={cn("flex shrink-0 items-center justify-center rounded-[28%] bg-sheet-soft text-sheet", size === "sm" ? "size-5" : "size-8", className)}>
    <FileSpreadsheet className={size === "sm" ? "size-3.5" : "size-4"} />
  </span>
}

/** A workbook file name: .xlsx or .xlsm. */
export const isSheetFileName = (name: string) => /\.xls[xm]$/i.test(name)
