import {
  LayoutDashboard, Bot, Network, Zap, Key, Activity,
  Shield, Settings,
} from "lucide-react"

export const navItems = [
  { label: "Dashboard", icon: LayoutDashboard, href: "/" },
  { label: "Crews", icon: Network, href: "/crews" },
  { label: "Agents", icon: Bot, href: "/agents", active: true },
  { label: "Skills", icon: Zap, href: "/skills" },
  { label: "Credentials", icon: Key, href: "/credentials" },
  { label: "Runs", icon: Activity, href: "/runs" },
  { label: "Audit Log", icon: Shield, href: "/audit" },
  { label: "Settings", icon: Settings, href: "/settings" },
]

export const agentTabs = ["Overview", "Sessions", "Files", "Runs", "Logs", "Skills", "Credentials", "Settings"]

export const mockSessions = [
  { id: "1", title: "Campaign performance review", status: "active", time: "2m ago", msgs: 12, unread: true },
  { id: "2", title: "Budget optimization Q1", status: "completed", time: "1h ago", msgs: 28, unread: false },
  { id: "3", title: "New keyword research", status: "active", time: "3h ago", msgs: 8, unread: true },
  { id: "4", title: "A/B test copy variants", status: "completed", time: "Yesterday", msgs: 15, unread: false },
  { id: "5", title: "Competitor analysis report", status: "completed", time: "2 days ago", msgs: 34, unread: false },
]

export const mockMessages = [
  { role: "user", text: "Can you review the campaign performance for last week?", time: "14:02" },
  { role: "agent", text: "I'll analyze the Google Ads data for last week. Here's what I found:\n\n- Total spend: $2,450\n- Impressions: 125K (+12%)\n- CTR: 3.2% (above benchmark)\n- Conversions: 48 (+8%)\n- CPA: $51.04", time: "14:03" },
  { role: "user", text: "That looks good. What about the underperforming ad groups?", time: "14:05" },
  { role: "agent", text: "Three ad groups are below target:\n\n1. \"Brand awareness\" - CPA $89 (target $60)\n2. \"Retargeting cold\" - CTR 0.8%\n3. \"Display network\" - 0 conversions\n\nI recommend pausing Display network and reallocating budget to top performers.", time: "14:06" },
]

