import http from "node:http";

const CREWSHIPD_URL = process.env.CREWSHIPD_URL ?? "unix:///tmp/crewship.sock";

interface RequestOptions {
  method?: string;
  body?: unknown;
  timeout?: number;
}

interface IPCSuccessResponse<T> {
  ok: true;
  status: number;
  data: T;
}

interface IPCErrorResponse {
  ok: false;
  status: number;
  error: string;
}

type IPCResponse<T = unknown> = IPCSuccessResponse<T> | IPCErrorResponse;

function parseSocketURL(url: string): { socketPath: string } | { host: string } {
  if (url.startsWith("unix://")) {
    return { socketPath: url.slice("unix://".length) };
  }
  return { host: url };
}

/**
 * Send an HTTP request to crewshipd over Unix socket or TCP.
 * Transport is auto-detected from CREWSHIPD_URL env var.
 */
export async function crewshipdRequest<T = unknown>(
  path: string,
  options: RequestOptions = {},
): Promise<IPCResponse<T>> {
  const { method = "GET", body, timeout = 30_000 } = options;
  const target = parseSocketURL(CREWSHIPD_URL);

  return new Promise((resolve, reject) => {
    const reqOptions: http.RequestOptions = {
      path,
      method,
      headers: { "Content-Type": "application/json" },
      timeout,
    };

    if ("socketPath" in target) {
      reqOptions.socketPath = target.socketPath;
    } else {
      const url = new URL(target.host);
      reqOptions.hostname = url.hostname;
      reqOptions.port = url.port;
    }

    const req = http.request(reqOptions, (res) => {
      let data = "";
      res.on("data", (chunk: Buffer) => {
        data += chunk.toString();
      });
      res.on("end", () => {
        const status = res.statusCode ?? 500;
        const ok = status < 400;

        try {
          const parsed = JSON.parse(data) as T;
          if (ok) {
            resolve({ ok: true, status, data: parsed });
          } else {
            resolve({ ok: false, status, error: data });
          }
        } catch {
          resolve({ ok: false, status, error: data });
        }
      });
    });

    req.on("error", reject);
    req.on("timeout", () => {
      req.destroy();
      reject(new Error(`IPC request to ${path} timed out after ${timeout}ms`));
    });

    if (body) {
      req.write(JSON.stringify(body));
    }
    req.end();
  });
}

/** Get live status of an agent from crewshipd. */
export async function getAgentStatus(agentId: string) {
  return crewshipdRequest<{ agent_id: string; status: string }>(
    `/agents/${encodeURIComponent(agentId)}/status`,
  );
}

/** Start an agent (Docker exec) via crewshipd. */
export async function startAgent(
  agentId: string,
  payload: { session_id: string; command?: string[] },
) {
  return crewshipdRequest<{ agent_id: string; status: string }>(
    `/agents/${encodeURIComponent(agentId)}/start`,
    { method: "POST", body: payload },
  );
}

/** Stop a running agent via crewshipd. */
export async function stopAgent(agentId: string) {
  return crewshipdRequest<{ agent_id: string; status: string }>(
    `/agents/${encodeURIComponent(agentId)}/stop`,
    { method: "POST" },
  );
}

/** Get Docker container status for a team. */
export async function getContainerStatus(teamId: string) {
  return crewshipdRequest<{ team_id: string; status: string }>(
    `/teams/${encodeURIComponent(teamId)}/container/status`,
  );
}

/** Start a team's Docker container. */
export async function startContainer(teamId: string) {
  return crewshipdRequest<{ team_id: string; status: string }>(
    `/teams/${encodeURIComponent(teamId)}/container/start`,
    { method: "POST" },
  );
}

/** Stop a team's Docker container. */
export async function stopContainer(teamId: string) {
  return crewshipdRequest<{ team_id: string; status: string }>(
    `/teams/${encodeURIComponent(teamId)}/container/stop`,
    { method: "POST" },
  );
}

/** List files in /output/ for a team, optionally filtered by agent slug. */
export async function getTeamFiles(teamId: string, agentSlug?: string) {
  const params = agentSlug ? `?agent_slug=${encodeURIComponent(agentSlug)}` : "";
  return crewshipdRequest<{ team_id: string; files: unknown[] }>(
    `/teams/${encodeURIComponent(teamId)}/files${params}`,
  );
}

/** Download a file from /output/ via IPC. Returns raw response for streaming. */
export async function downloadTeamFile(teamId: string, filePath: string) {
  return crewshipdRequest<Buffer>(
    `/teams/${encodeURIComponent(teamId)}/files/download?path=${encodeURIComponent(filePath)}`,
  );
}

/** Read JSONL conversation messages for a session. */
export async function getSessionMessages(
  sessionId: string,
  offset = 0,
  limit = 50,
) {
  return crewshipdRequest<{ session_id: string; messages: unknown[] }>(
    `/sessions/${encodeURIComponent(sessionId)}/messages?offset=${offset}&limit=${limit}`,
  );
}

/** Create a conversation session in Prisma via crewshipd relay. */
export async function createSession(params: {
  session_id: string;
  agent_id: string;
  org_id: string;
  user_id?: string;
  title?: string;
}) {
  return crewshipdRequest<{ id: string; status: string }>("/sessions", {
    method: "POST",
    body: params,
  });
}

/** Read agent logs from crewshipd. */
export async function getAgentLogs(
  agentId: string,
  teamId: string,
  offset = 0,
  limit = 100,
) {
  return crewshipdRequest<{
    agent_id: string;
    logs: Array<{
      ts: string;
      level: string;
      agent: string;
      event: string;
      content?: string;
    }>;
  }>(
    `/agents/${encodeURIComponent(agentId)}/logs?team_id=${encodeURIComponent(teamId)}&offset=${offset}&limit=${limit}`,
  );
}

/** Get crewshipd service logs from ring buffer. */
export async function getDebugLogs(limit = 200, agentId?: string) {
  let url = `/debug/logs?limit=${limit}`;
  if (agentId) url += `&agent_id=${encodeURIComponent(agentId)}`;
  return crewshipdRequest<{
    logs: Array<{
      time: string;
      level: string;
      msg: string;
      attrs?: Record<string, string>;
    }>;
  }>(url);
}

/** Get comprehensive debug info from crewshipd. */
export async function getDebugInfo() {
  return crewshipdRequest<{
    status: string;
    uptime: string;
    uptime_secs: number;
    connections: number;
    started_at: string;
    providers: Record<string, string>;
    container_available: boolean;
    storage_available: boolean;
    state_available: boolean;
    llm_proxy_enabled: boolean;
    config: Record<string, unknown>;
  }>("/debug/info");
}

/** Check if crewshipd is running and healthy. */
export async function healthCheck() {
  return crewshipdRequest<{ status: string }>("/health");
}
