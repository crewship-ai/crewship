"use client"

import { FileQuestion } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import Link from "next/link"

export default function NotFound() {
  return (
    <div className="flex min-h-screen items-center justify-center p-6 bg-background">
      <Card className="w-full max-w-md">
        <CardContent className="flex flex-col items-center py-10 text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-muted mb-4">
            <FileQuestion className="h-6 w-6 text-muted-foreground" />
          </div>
          <h2 className="text-lg font-semibold">Page Not Found</h2>
          <p className="mt-1 text-sm text-muted-foreground max-w-sm">
            The page you are looking for does not exist or has been moved.
          </p>
          <Button asChild className="mt-6">
            <Link href="/">Go to Dashboard</Link>
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}
