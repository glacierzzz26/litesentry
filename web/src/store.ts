// 全局状态：登录态 + 当前用户 + 节点列表 + 服务健康（mock 模式）。
// 登录态以 localStorage 里的 JWT 为底；首登/重置后 must_change_password 触发强制改密流程。

import { create } from 'zustand';
import { api, getToken, setOnAuthExpired, setToken } from './api';
import type { Agent, User } from './types';

interface AppState {
  authed: boolean;
  user: User | null;
  changePwd: boolean; // 首登/重置密码后未完成强制改密
  agents: Agent[];
  agentsLoading: boolean;
  serverOk: boolean;
  login: (username: string, password: string) => Promise<string | null>; // null = 成功，否则错误消息
  changePassword: (oldPwd: string, newPwd: string) => Promise<string | null>;
  logout: () => void;
  restoreUser: () => Promise<void>;
  refreshAgents: () => Promise<void>;
  checkHealth: () => Promise<void>;
}

export const useStore = create<AppState>((set) => ({
  authed: !!getToken(),
  user: null,
  changePwd: false,
  agents: [],
  agentsLoading: false,
  serverOk: true,

  login: async (username: string, password: string) => {
    try {
      const res = await api.login(username, password);
      setToken(res.token);
      set({ authed: true, user: res.user, changePwd: res.must_change_password, serverOk: true });
      return null;
    } catch (e) {
      return String(e).replace(/^Error:\s*/, '') || '登录失败';
    }
  },

  changePassword: async (oldPwd: string, newPwd: string) => {
    try {
      await api.changePassword(oldPwd, newPwd);
      set({ changePwd: false });
      return null;
    } catch (e) {
      return String(e).replace(/^Error:\s*/, '');
    }
  },

  logout: () => {
    setToken('');
    set({ authed: false, user: null, changePwd: false, agents: [] });
  },

  restoreUser: async () => {
    try {
      const u = await api.me();
      set({ user: u, changePwd: u.must_change_password });
    } catch {
      /* mock/网络异常静默 */
    }
  },

  refreshAgents: async () => {
    set({ agentsLoading: true });
    try {
      const agents = await api.agents();
      set({ agents, serverOk: true });
    } catch {
      set({ serverOk: false });
    } finally {
      set({ agentsLoading: false });
    }
  },

  checkHealth: async () => {
    try {
      await api.health();
      set({ serverOk: true });
    } catch {
      set({ serverOk: false });
    }
  },
}));

// token 过期/失效（任意受保护接口 401）→ 自动登出回登录页
setOnAuthExpired(() => useStore.getState().logout());
