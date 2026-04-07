"use client"

import { useCallback, useEffect, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Key, Plus, Copy, Check, Trash2, AlertTriangle, Clock, Loader2, Terminal, Eye, EyeOff } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Separator } from "@/components/ui/separator"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { EmptyState } from "@/components/layout/empty-state"
import { cn } from "@/lib/utils"

interface CLIToken {
  id: string
  name: string
  created_at: string
  last_used_at?: string
  revoked_at?: string
}

function timeAgo(dateStr: string): string {
  const diff = Date.now() - new Date(dateStr).getTime()
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return "just now"
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  return new Date(dateStr).toLocaleDateString("en-US", { month: "short", day: "numeric" })
}

interface ApiTokensSectionProps {
  workspaceId: string
}

export function ApiTokensSection({ workspaceId: _workspaceId }: ApiTokensSectionProps) {
  const [tokens, setTokens] = useState<CLIToken[]>([])
  const [loading, setLoading] = useState(true)
  const [creating, setCreating] = useState(false)
  const [tokenName, setTokenName] = useState("")
  const [showCreateForm, setShowCreateForm] = useState(false)

  // Newly created token (shown once, then hidden)
  const [newToken, setNewToken] = useState<{ token: string; name: string } | null>(null)
  const [copied, setCopied] = useState(false)
  const [tokenVisible, setTokenVisible] = useState(false)

  // Revoke dialog
  const [revokeTarget, setRevokeTarget] = useState<CLIToken | null>(null)
  const [revoking, setRevoking] = useState(false)

  const fetchTokens = useCallback(async () => {
    try {
      const res = await fetch("/api/v1/auth/cli-tokens")
      if (res.ok) {
        const data = await res.json()
        setTokens(data.data ?? [])
      }
    } catch { /* ignore */ }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { fetchTokens() }, [fetchTokens])

  async function handleCreate() {
    if (!tokenName.trim()) return
    setCreating(true)
    try {
      const res = await fetch("/api/v1/auth/cli-token", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: tokenName.trim() }),
      })
      if (res.ok) {
        const data = await res.json()
        setNewToken({ token: data.token, name: data.name })
        setTokenName("")
        setShowCreateForm(false)
        setTokenVisible(false)
        setCopied(false)
        fetchTokens()
      }
    } catch { /* ignore */ }
    finally { setCreating(false) }
  }

  async function handleRevoke() {
    if (!revokeTarget) return
    setRevoking(true)
    try {
      const res = await fetch(`/api/v1/auth/cli-tokens/${revokeTarget.id}`, { method: "DELETE" })
      if (res.ok) {
        fetchTokens()
      }
    } catch { /* ignore */ }
    finally {
      setRevoking(false)
      setRevokeTarget(null)
    }
  }

  function handleCopy(text: string) {
    navigator.clipboard.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  const activeTokens = tokens.filter((t) => !t.revoked_at)
  const revokedTokens = tokens.filter((t) => t.revoked_at)

  return (
    <div className="space-y-5">
      {/* New token reveal banner */}
      <AnimatePresence>
        {newToken && (
          <motion.div
            initial={{ opacity: 0, y: -12, scale: 0.98 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, y: -8, scale: 0.98 }}
            transition={{ duration: 0.2, ease: "easeOut" }}
          >
            <Card className="border-emerald-500/30 bg-emerald-500/[0.04]">
              <CardContent className="p-5">
                <div className="flex items-start gap-3 mb-3">
                  <div className="w-8 h-8 rounded-lg bg-emerald-500/15 flex items-center justify-center shrink-0">
                    <Key className="h-4 w-4 text-emerald-400" />
                  </div>
                  <div className="min-w-0 flex-1">
                    <h4 className="text-[14px] font-semibold text-emerald-400">Token created</h4>
                    <p className="text-[12px] text-emerald-400/50 mt-0.5">
                      Copy this token now — it won&apos;t be shown again.
                    </p>
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 text-[11px] text-muted-foreground/50 hover:text-muted-foreground shrink-0"
                    onClick={() => setNewToken(null)}
                  >
                    Dismiss
                  </Button>
                </div>

                {/* Token display */}
                <div className="bg-black/40 border border-emerald-500/20 rounded-lg p-3 flex items-center gap-2">
                  <Terminal className="h-3.5 w-3.5 text-emerald-500/40 shrink-0" />
                  <code className="flex-1 text-[12px] font-mono text-emerald-300/90 break-all select-all leading-relaxed">
                    {tokenVisible ? newToken.token : newToken.token.slice(0, 18) + "•".repeat(30)}
                  </code>
                  <div className="flex items-center gap-1 shrink-0">
                    <TooltipProvider delayDuration={0}>
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7 text-emerald-400/60 hover:text-emerald-400"
                            onClick={() => setTokenVisible(!tokenVisible)}
                          >
                            {tokenVisible ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent side="top" className="text-xs">{tokenVisible ? "Hide" : "Reveal"}</TooltipContent>
                      </Tooltip>
                    </TooltipProvider>
                    <Button
                      variant="ghost"
                      size="sm"
                      className={cn(
                        "h-7 gap-1.5 text-[11px] font-medium",
                        copied ? "text-emerald-400" : "text-emerald-400/60 hover:text-emerald-400",
                      )}
                      onClick={() => handleCopy(newToken.token)}
                    >
                      {copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
                      {copied ? "Copied" : "Copy"}
                    </Button>
                  </div>
                </div>

                {/* Usage hint */}
                <div className="mt-3 bg-black/20 rounded-md p-2.5 border border-white/[0.04]">
                  <p className="text-[11px] text-muted-foreground/50 font-mono">
                    <span className="text-muted-foreground/30">$</span>{" "}
                    <span className="text-foreground/60">crewship</span>{" "}
                    <span className="text-blue-400/70">auth login</span>{" "}
                    <span className="text-muted-foreground/30">--token</span>{" "}
                    <span className="text-emerald-400/50">&lt;token&gt;</span>
                  </p>
                </div>
              </CardContent>
            </Card>
          </motion.div>
        )}
      </AnimatePresence>

      {/* Create token */}
      <Card className="border-white/[0.06]">
        <CardContent className="p-5">
          <div className="flex items-center justify-between">
            <div>
              <h4 className="text-[14px] font-medium text-foreground">CLI Tokens</h4>
              <p className="text-[12px] text-muted-foreground/40 mt-0.5">
                Authenticate the Crewship CLI without a browser login.
              </p>
            </div>
            {!showCreateForm && (
              <Button
                size="sm"
                className="h-7 gap-1.5 text-[11px]"
                onClick={() => setShowCreateForm(true)}
              >
                <Plus className="h-3 w-3" />
                New Token
              </Button>
            )}
          </div>

          {/* Create form */}
          <AnimatePresence>
            {showCreateForm && (
              <motion.div
                initial={{ opacity: 0, height: 0 }}
                animate={{ opacity: 1, height: "auto" }}
                exit={{ opacity: 0, height: 0 }}
                transition={{ duration: 0.15 }}
                className="overflow-hidden"
              >
                <Separator className="bg-white/[0.06] my-4" />
                <div className="flex items-end gap-3">
                  <div className="flex-1 space-y-1.5">
                    <label className="text-[11px] text-muted-foreground/50 uppercase tracking-wider">
                      Token name
                    </label>
                    <Input
                      value={tokenName}
                      onChange={(e) => setTokenName(e.target.value)}
                      placeholder="e.g. MacBook Pro, CI/CD pipeline"
                      className="h-9 bg-white/[0.03] border-white/[0.08] text-[13px]"
                      onKeyDown={(e) => e.key === "Enter" && handleCreate()}
                      autoFocus
                    />
                  </div>
                  <div className="flex gap-2 shrink-0">
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-9 text-[12px] text-muted-foreground"
                      onClick={() => { setShowCreateForm(false); setTokenName("") }}
                    >
                      Cancel
                    </Button>
                    <Button
                      size="sm"
                      className="h-9 gap-1.5 text-[12px]"
                      onClick={handleCreate}
                      disabled={creating || !tokenName.trim()}
                    >
                      {creating && <Loader2 className="h-3 w-3 animate-spin" />}
                      Create
                    </Button>
                  </div>
                </div>
              </motion.div>
            )}
          </AnimatePresence>
        </CardContent>
      </Card>

      {/* Token list */}
      {loading ? (
        <Card className="border-white/[0.06]">
          <CardContent className="p-8 text-center">
            <Loader2 className="h-5 w-5 animate-spin text-muted-foreground/30 mx-auto" />
          </CardContent>
        </Card>
      ) : activeTokens.length === 0 && revokedTokens.length === 0 ? (
        <Card className="border-white/[0.06]">
          <CardContent className="py-12">
            <EmptyState
              icon={Key}
              title="No API tokens"
              description="Create a token to authenticate the Crewship CLI"
            />
          </CardContent>
        </Card>
      ) : (
        <>
          {/* Active tokens */}
          {activeTokens.length > 0 && (
            <div className="space-y-1.5">
              {activeTokens.map((token, i) => (
                <motion.div
                  key={token.id}
                  initial={{ opacity: 0, y: 8 }}
                  animate={{ opacity: 1, y: 0 }}
                  transition={{ duration: 0.15, delay: i * 0.03 }}
                >
                  <Card className="border-white/[0.06] hover:border-white/[0.1] transition-colors">
                    <CardContent className="p-4">
                      <div className="flex items-center gap-3">
                        {/* Status indicator */}
                        <div className="w-8 h-8 rounded-lg bg-emerald-500/10 flex items-center justify-center shrink-0">
                          <Key className="h-3.5 w-3.5 text-emerald-400/70" />
                        </div>

                        {/* Info */}
                        <div className="flex-1 min-w-0">
                          <div className="flex items-center gap-2">
                            <span className="text-[13px] font-medium text-foreground truncate">
                              {token.name}
                            </span>
                            <Badge variant="outline" className="text-[9px] border-emerald-500/20 text-emerald-400/70 shrink-0">
                              Active
                            </Badge>
                          </div>
                          <div className="flex items-center gap-3 mt-1">
                            <span className="text-[11px] text-muted-foreground/30 font-mono flex items-center gap-1">
                              <Clock className="h-2.5 w-2.5" />
                              Created {timeAgo(token.created_at)}
                            </span>
                            {token.last_used_at && (
                              <span className="text-[11px] text-muted-foreground/30 font-mono">
                                Used {timeAgo(token.last_used_at)}
                              </span>
                            )}
                          </div>
                        </div>

                        {/* Revoke */}
                        <TooltipProvider delayDuration={0}>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="icon"
                                className="h-8 w-8 text-muted-foreground/30 hover:text-red-400 hover:bg-red-500/10"
                                onClick={() => setRevokeTarget(token)}
                              >
                                <Trash2 className="h-3.5 w-3.5" />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent side="left" className="text-xs">Revoke token</TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      </div>
                    </CardContent>
                  </Card>
                </motion.div>
              ))}
            </div>
          )}

          {/* Revoked tokens */}
          {revokedTokens.length > 0 && (
            <div className="space-y-1.5">
              <div className="text-[10px] font-semibold text-muted-foreground/20 uppercase tracking-wider px-1 pt-2">
                Revoked
              </div>
              {revokedTokens.map((token) => (
                <Card key={token.id} className="border-white/[0.04] opacity-50">
                  <CardContent className="p-4">
                    <div className="flex items-center gap-3">
                      <div className="w-8 h-8 rounded-lg bg-white/[0.03] flex items-center justify-center shrink-0">
                        <Key className="h-3.5 w-3.5 text-muted-foreground/30" />
                      </div>
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="text-[13px] text-muted-foreground/50 line-through truncate">
                            {token.name}
                          </span>
                          <Badge variant="outline" className="text-[9px] border-white/[0.06] text-muted-foreground/30 shrink-0">
                            Revoked
                          </Badge>
                        </div>
                        <div className="flex items-center gap-3 mt-1">
                          <span className="text-[11px] text-muted-foreground/20 font-mono">
                            Revoked {token.revoked_at ? timeAgo(token.revoked_at) : ""}
                          </span>
                        </div>
                      </div>
                    </div>
                  </CardContent>
                </Card>
              ))}
            </div>
          )}
        </>
      )}

      {/* Revoke confirmation */}
      <AlertDialog open={!!revokeTarget} onOpenChange={(open) => !open && setRevokeTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="flex items-center gap-2">
              <AlertTriangle className="h-4 w-4 text-red-400" />
              Revoke token
            </AlertDialogTitle>
            <AlertDialogDescription>
              Revoke <strong>{revokeTarget?.name}</strong>? Any CLI sessions using this token
              will be immediately disconnected. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleRevoke}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              disabled={revoking}
            >
              {revoking && <Loader2 className="h-3.5 w-3.5 animate-spin mr-1.5" />}
              Revoke
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
