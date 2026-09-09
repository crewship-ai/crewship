import type { PagePublication } from "@/hooks/use-page-application"

export function publicationReceiptMessage(receipt: PagePublication): string {
  if (receipt.is_current === false) {
    return receipt.published
      ? `Version ${receipt.version} was published earlier. The current live version is ${receipt.live_version}.`
      : `Version ${receipt.version} was published earlier. The application is now withdrawn.`
  }
  return `Published version ${receipt.version}.`
}
