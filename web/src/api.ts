// 轻量数据客户端 —— 直连 Go/Gin 后端 REST（同源 /api，开发环境由 vite 代理到 :8080）。
// 登录态存 JWT（localStorage），所有请求自动注入 Authorization: Bearer。
// 非 2xx 解析 {error} 抛出带后端中文消息的错误；非登录请求遇 401（token 过期/失效）自动登出。

import type {
  Agent,
  AlertEvent,
  AlertRule,
  ContainerSample,
  DiskSample,
  FrpConfigView,
  HostSample,
  LoginResult,
  Overview,
  SaveFrpBody,
  SettingsBody,
  SettingsView,
  User,
} from './types';
import { latestByKey } from './types';

const TOKEN_KEY = 'litesentry_jwt';

export function getToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? '';
  } catch {
    return '';
  }
}

export function setToken(t: string) {
  try {
    if (t) localStorage.setItem(TOKEN_KEY, t);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    /* 隐私模式下静默失败 */
  }
}

/** 最近 N 小时窗口的 unix 秒。 */
export function hoursAgo(h: number): number {
  return Math.floor(Date.now() / 1000) - h * 3600;
}

export interface EventsQuery {
  from?: number;
  to?: number;
  agent_id?: string;
  state?: string;
  limit?: number;
}

// ---- 401（token 过期/失效）自动登出回调，由 store 注册 ----
let onAuthExpired: (() => void) | null = null;
export function setOnAuthExpired(fn: (() => void) | null) {
  onAuthExpired = fn;
}

/** 查询串：跳过 undefined / 空串。 */
function qs(params: Record<string, string | number | undefined>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : '';
}

/** 请求统一封装：注入 JWT、解析 {error}、401 自动登出、网络错误中文化。 */
async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.method === 'POST' || init.method === 'PUT' || init.body) {
    headers.set('Content-Type', 'application/json');
  }
  const token = getToken();
  if (token) headers.set('Authorization', `Bearer ${token}`);

  let res: Response;
  try {
    res = await fetch(path, { ...init, headers });
  } catch {
    throw new Error('无法连接服务器');
  }

  // token 过期/无效：清本地 token 并回到登录页（登录接口本身的 401 除外）
  if (res.status === 401 && !path.startsWith('/api/auth/login')) {
    setToken('');
    onAuthExpired?.();
  }

  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const body = await res.json();
      if (body && typeof body.error === 'string') msg = body.error;
    } catch {
      /* 非 JSON 响应，保留默认 */
    }
    throw new Error(msg);
  }
  return (await res.json()) as T;
}

const get = <T,>(path: string) => request<T>(path);
const post = <T,>(path: string, body?: unknown) => request<T>(path, { method: 'POST', body: JSON.stringify(body ?? {}) });
const put = <T,>(path: string, body: unknown) => request<T>(path, { method: 'PUT', body: JSON.stringify(body) });
const del = <T,>(path: string) => request<T>(path, { method: 'DELETE' });

export const api = {
  // ---- 认证 / 用户 ----
  login: (username: string, password: string): Promise<LoginResult> =>
    post<LoginResult>('/api/auth/login', { username, password }),
  changePassword: (oldPassword: string, newPassword: string): Promise<{ status: string }> =>
    post<{ status: string }>('/api/auth/change-password', { old_password: oldPassword, new_password: newPassword }),
  me: () => get<User>('/api/auth/me'),
  users: () => get<User[]>('/api/users'),
  createUser: (b: { username: string; password: string; display_name?: string }) => post<User>('/api/users', b),
  updateUser: (id: string, b: { display_name?: string; role?: string }) =>
    put<User>(`/api/users/${encodeURIComponent(id)}`, b),
  resetPassword: (id: string, password: string) =>
    put<User>(`/api/users/${encodeURIComponent(id)}/password`, { password }),
  deleteUser: (id: string) => del<{ status: string }>(`/api/users/${encodeURIComponent(id)}`),

  // ---- 监控数据 ----
  health: () => get<{ status: string; time: string }>('/api/health'),
  agents: () => get<Agent[]>('/api/agents'),

  host: (id: string, from?: number, to?: number) =>
    get<HostSample[]>(`/api/agents/${encodeURIComponent(id)}/host${qs({ from, to })}`),
  containers: (id: string, from?: number, to?: number) =>
    get<ContainerSample[]>(`/api/agents/${encodeURIComponent(id)}/containers${qs({ from, to })}`),
  disks: (id: string, from?: number, to?: number) =>
    get<DiskSample[]>(`/api/agents/${encodeURIComponent(id)}/disks${qs({ from, to })}`),

  overview: (from?: number, to?: number) => get<Overview>(`/api/overview${qs({ from, to })}`),

  // 全部容器（跨节点）：无聚合端点，按 agents 扇出后每容器取最近一条。
  allContainers: async (from?: number): Promise<ContainerSample[]> => {
    const agents = await get<Agent[]>('/api/agents');
    const f = from ?? hoursAgo(1);
    const to = Math.floor(Date.now() / 1000);
    const all = await Promise.all(
      agents.map((a) =>
        get<ContainerSample[]>(`/api/agents/${encodeURIComponent(a.agent_id)}/containers${qs({ from: f, to })}`).catch(() => []),
      ),
    );
    return [...latestByKey(all.flat(), (c) => `${c.agent_id}/${c.container_id}`).values()];
  },

  rules: () => get<AlertRule[]>('/api/alerts/rules'),
  createRule: (r: Partial<AlertRule>) => post<AlertRule>('/api/alerts/rules', r),
  updateRule: (id: string, r: Partial<AlertRule>) => put<AlertRule>(`/api/alerts/rules/${encodeURIComponent(id)}`, r),
  deleteRule: (id: string) => del<{ status: string }>(`/api/alerts/rules/${encodeURIComponent(id)}`),

  events: (q: EventsQuery = {}) =>
    get<AlertEvent[]>(
      `/api/alerts/events${qs({ from: q.from, to: q.to, agent_id: q.agent_id || undefined, state: q.state || undefined, limit: q.limit })}`,
    ),

  settings: () => get<SettingsView>('/api/settings'),
  saveSettings: (b: SettingsBody) => put<SettingsView>('/api/settings', b),
  feishuTest: () => post<{ status: string; message: string }>('/api/settings/feishu-test'),

  // ---- FRP 隧道 ----
  frpConfigs: () => get<FrpConfigView[]>('/api/frp'),
  saveFrpConfig: (kind: string, agentId: string, b: SaveFrpBody) =>
    put<FrpConfigView>(`/api/frp/${encodeURIComponent(kind)}/${encodeURIComponent(agentId)}`, b),
  deleteFrpConfig: (kind: string, agentId: string) =>
    del<{ status: string }>(`/api/frp/${encodeURIComponent(kind)}/${encodeURIComponent(agentId)}`),
};

// 类型再导出，页面层直接从 api 引用也行
export type {
  Agent,
  AlertEvent,
  AlertRule,
  ContainerSample,
  DiskSample,
  FrpConfigView,
  HostSample,
  LoginResult,
  Overview,
  SaveFrpBody,
  SettingsBody,
  SettingsView,
  User,
};
