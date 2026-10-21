import { redirect } from "next/navigation"

// The Paymaster page became Activity's Spend view. Old bookmarks and
// entityHref links land here and are bounced over instead of a Not found
// (#3618). Server-side, like /runs, so nothing renders first.
export default function PaymasterRedirect(): never {
  redirect("/activity?mode=spend")
}
