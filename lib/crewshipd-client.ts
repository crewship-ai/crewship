import http from "node:http";

const CREWSHIPD_URL = process.env.CREWSHIPD_URL ?? "unix:///tmp/crewship.sock";

interface RequestOptions {
  method?: string;
  body?: unknown;
  timeout?: number;
}

interface IPCResponse<T = unknown> {
  ok: boolean;
  status: number;
  data: T;
}

function parseSocketURL(url: string): { socketPath: string } | { host: string } {
  if (url.startsWith("unix://")) {
    return { socketPath: url.slice("unix://".length) };
  }
  return { host: url };
}

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
        try {
          const parsed = JSON.parse(data) as T;
          resolve({
            ok: (res.statusCode ?? 500) < 400,
            status: res.statusCode ?? 500,
            data: parsed,
          });
        } catch {
          resolve({
            ok: false,
            status: res.statusCode ?? 500,
            data: data as unknown as T,
          });
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

export async function getAgentStatus(agentId: string) {
  return crewshipdRequest<{ agent_id: string; status: string }>(
    `/agents/${agentId}/status`,
  );
}

export async function startAgent(
  agentId: string,
  payload: { session_id: string; command?: string[] },
) {
  return crewshipdRequest<{ agent_id: string; status: string }>(
    `/agents/${agentId}/start`,
    { method: "POST", body: payload },
  );
}

export async function stopAgent(agentId: string) {
  return crewshipdRequest<{ agent_id: string; status: string }>(
    `/agents/${agentId}/stop`,
    { method: "POST" },
  );
}

export async function getContainerStatus(teamId: string) {
  return crewshipdRequest<{ team_id: string; status: string }>(
    `/teams/${teamId}/container/status`,
  );
}

export async function startContainer(teamId: string) {
  return crewshipdRequest<{ team_id: string; status: string }>(
    `/teams/${teamId}/container/start`,
    { method: "POST" },
  );
}

export async function stopContainer(teamId: string) {
  return crewshipdRequest<{ team_id: string; status: string }>(
    `/teams/${teamId}/container/stop`,
    { method: "POST" },
  );
}

export async function getTeamFiles(teamId: string) {
  return crewshipdRequest<{ team_id: string; files: unknown[] }>(
    `/teams/${teamId}/files`,
  );
}

export async function getSessionMessages(
  sessionId: string,
  offset = 0,
  limit = 50,
) {
  return crewshipdRequest<{ session_id: string; messages: unknown[] }>(
    `/sessions/${sessionId}/messages?offset=${offset}&limit=${limit}`,
  );
}

export async function healthCheck() {
  return crewshipdRequest<{ status: string }>("/health");
}
