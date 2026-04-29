/* Mock data + pure helpers shared by preview-files variants. */

export interface TreeNode {
  name: string
  path: string
  isDir: boolean
  size: number
  modTime: string
  children: TreeNode[]
}


export const MOCK_TREE: TreeNode[] = [
  {
    name: "google-ads-env", path: "google-ads-env", isDir: true, size: 0, modTime: "2025-03-11T10:00:00Z",
    children: [
      { name: ".env", path: "google-ads-env/.env", isDir: false, size: 245, modTime: "2025-03-11T10:05:00Z", children: [] },
      { name: "config.yaml", path: "google-ads-env/config.yaml", isDir: false, size: 1024, modTime: "2025-03-11T09:30:00Z", children: [] },
      { name: "requirements.txt", path: "google-ads-env/requirements.txt", isDir: false, size: 312, modTime: "2025-03-10T15:20:00Z", children: [] },
    ],
  },
  {
    name: "google-ads-python-main", path: "google-ads-python-main", isDir: true, size: 0, modTime: "2025-03-11T09:00:00Z",
    children: [
      { name: "main.py", path: "google-ads-python-main/main.py", isDir: false, size: 4520, modTime: "2025-03-11T10:12:00Z", children: [] },
      { name: "campaign_manager.py", path: "google-ads-python-main/campaign_manager.py", isDir: false, size: 8340, modTime: "2025-03-11T10:08:00Z", children: [] },
      { name: "utils.py", path: "google-ads-python-main/utils.py", isDir: false, size: 1280, modTime: "2025-03-10T18:00:00Z", children: [] },
      {
        name: "tests", path: "google-ads-python-main/tests", isDir: true, size: 0, modTime: "2025-03-10T16:00:00Z",
        children: [
          { name: "test_campaigns.py", path: "google-ads-python-main/tests/test_campaigns.py", isDir: false, size: 2100, modTime: "2025-03-10T16:30:00Z", children: [] },
          { name: "conftest.py", path: "google-ads-python-main/tests/conftest.py", isDir: false, size: 540, modTime: "2025-03-10T16:00:00Z", children: [] },
        ],
      },
    ],
  },
  {
    name: "googleads_env", path: "googleads_env", isDir: true, size: 0, modTime: "2025-03-09T12:00:00Z",
    children: [
      { name: "activate.sh", path: "googleads_env/activate.sh", isDir: false, size: 890, modTime: "2025-03-09T12:00:00Z", children: [] },
      {
        name: "lib", path: "googleads_env/lib", isDir: true, size: 0, modTime: "2025-03-09T12:00:00Z",
        children: [
          { name: "python3.11", path: "googleads_env/lib/python3.11", isDir: true, size: 0, modTime: "2025-03-09T12:00:00Z", children: [] },
        ],
      },
    ],
  },
  { name: "NAVOD.md", path: "NAVOD.md", isDir: false, size: 2048, modTime: "2025-03-11T10:15:00Z", children: [] },
  { name: "report.json", path: "report.json", isDir: false, size: 15360, modTime: "2025-03-11T10:20:00Z", children: [] },
  { name: "Dockerfile", path: "Dockerfile", isDir: false, size: 640, modTime: "2025-03-08T14:00:00Z", children: [] },
  { name: ".gitignore", path: ".gitignore", isDir: false, size: 128, modTime: "2025-03-08T14:00:00Z", children: [] },
]


export const MOCK_PREVIEW_CODE = `import os
from google.ads.googleads.client import GoogleAdsClient
from google.ads.googleads.errors import GoogleAdsException

class CampaignManager:
    """Manages Google Ads campaigns for the agent."""

    def __init__(self, credentials_path: str):
        self.client = GoogleAdsClient.load_from_storage(credentials_path)
        self.customer_id = os.getenv("GOOGLE_ADS_CUSTOMER_ID")

    def list_campaigns(self, status_filter: str = "ENABLED"):
        """List all campaigns with optional status filter."""
        ga_service = self.client.get_service("GoogleAdsService")
        query = f"""
            SELECT
                campaign.id,
                campaign.name,
                campaign.status,
                metrics.impressions,
                metrics.clicks,
                metrics.cost_micros
            FROM campaign
            WHERE campaign.status = '{status_filter}'
            ORDER BY metrics.impressions DESC
        """
        response = ga_service.search(
            customer_id=self.customer_id,
            query=query
        )
        campaigns = []
        for row in response:
            campaigns.append({
                "id": row.campaign.id,
                "name": row.campaign.name,
                "status": row.campaign.status.name,
                "impressions": row.metrics.impressions,
                "clicks": row.metrics.clicks,
                "cost": row.metrics.cost_micros / 1_000_000,
            })
        return campaigns

    def pause_campaign(self, campaign_id: str):
        """Pause a running campaign."""
        campaign_service = self.client.get_service("CampaignService")
        campaign_operation = self.client.get_type("CampaignOperation")
        campaign = campaign_operation.update
        campaign.resource_name = campaign_service.campaign_path(
            self.customer_id, campaign_id
        )
        campaign.status = self.client.enums.CampaignStatusEnum.PAUSED
        campaign_service.mutate_campaigns(
            customer_id=self.customer_id,
            operations=[campaign_operation],
        )
        return {"status": "paused", "campaign_id": campaign_id}
`


export const MOCK_GIT_LOG = [
  { hash: "a1b2c3d", message: "feat: add campaign pause functionality", author: "Pepicek", time: "2 hours ago", branch: "main" },
  { hash: "e4f5g6h", message: "fix: handle empty API response gracefully", author: "Pepicek", time: "5 hours ago", branch: "main" },
  { hash: "i7j8k9l", message: "refactor: extract utils into separate module", author: "Pepicek", time: "1 day ago", branch: "main" },
  { hash: "m0n1o2p", message: "init: Google Ads campaign manager", author: "Pepicek", time: "2 days ago", branch: "main" },
]


export function formatSize(bytes: number): string {
  if (bytes === 0) return "—"
  const units = ["B", "KB", "MB", "GB"]
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  const v = bytes / Math.pow(1024, i)
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}


export function timeAgo(iso: string): string {
  const mins = Math.floor((Date.now() - new Date(iso).getTime()) / 60000)
  if (mins < 1) return "Just now"
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  const days = Math.floor(hrs / 24)
  return days === 1 ? "Yesterday" : `${days}d ago`
}


export function countFiles(nodes: TreeNode[]): { files: number; dirs: number; size: number } {
  let files = 0, dirs = 0, size = 0
  for (const n of nodes) {
    if (n.isDir) { dirs++; const sub = countFiles(n.children); files += sub.files; dirs += sub.dirs; size += sub.size }
    else { files++; size += n.size }
  }
  return { files, dirs, size }
}


export function findNode(nodes: TreeNode[], path: string): TreeNode | null {
  for (const n of nodes) {
    if (n.path === path) return n
    if (n.isDir) { const f = findNode(n.children, path); if (f) return f }
  }
  return null
}

