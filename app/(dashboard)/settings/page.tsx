import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"

export default function SettingsPage() {
  return (
    <div className="p-6 space-y-6 max-w-2xl">
      <PageHeader title="Settings" description="Manage your organization settings" />

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Organization</CardTitle>
          <CardDescription>Update your organization details</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="org-name">Organization Name</Label>
            <Input id="org-name" placeholder="My Company" />
          </div>
          <div className="space-y-2">
            <Label htmlFor="org-slug">Slug</Label>
            <Input id="org-slug" placeholder="my-company" />
          </div>
          <Button>Save Changes</Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Danger Zone</CardTitle>
          <CardDescription>Irreversible actions for your organization</CardDescription>
        </CardHeader>
        <CardContent>
          <Button variant="destructive">Delete Organization</Button>
        </CardContent>
      </Card>
    </div>
  )
}
