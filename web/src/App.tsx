// v2 应用壳 —— 按 litesentry-prototype-v2.html 重建：
//   左侧固定侧栏（品牌 + 图标导航 + 折叠）、顶部细顶栏（标题 + 在线状态点 + 主题切换 + 用户 + 退出）、
//   内容区卡片网格。路由复用 src/route 的 hash 方案，登录/改密门复用 src 页面。
// 复用：src/api · src/store · src/types · src/route · src/components 与既有完整页面。

import { useEffect, useState } from 'react';
import { App as AntApp, ConfigProvider, Tooltip } from 'antd';
import {
  AppstoreOutlined,
  ControlOutlined,
  DeploymentUnitOutlined,
  DesktopOutlined,
  LinkOutlined,
  MoonOutlined,
  SettingOutlined,
  SunOutlined,
  WarningOutlined,
} from '@ant-design/icons';
import zhCN from 'antd/locale/zh_CN';
import { useHashRoute, navigate } from './route';
import { useStore } from './store';
import { THEME_LIGHT, THEME_DARK, currentTheme, applyTheme } from './theme';
// 复用既有完整页面（antd + 真实 API）
import Login from './pages/Login';
import ChangePassword from './pages/ChangePassword';
import Settings from './pages/Settings';
import AlertRules from './pages/AlertRules';
import Frp from './pages/Frp';
// v2 新建页面
import Overview from './pages/Overview';
import Hosts from './pages/Hosts';
import AgentDetail from './pages/AgentDetail';
import Containers from './pages/Containers';
import ContainerDetail from './pages/ContainerDetail';
import Alerts from './pages/Alerts';

const MENU = [
  { key: '/', icon: <AppstoreOutlined />, label: '总览' },
  { key: '/hosts', icon: <DesktopOutlined />, label: '主机' },
  { key: '/containers', icon: <DeploymentUnitOutlined />, label: '容器' },
  { key: '/alerts', icon: <WarningOutlined />, label: '告警事件' },
  { key: '/alerts/rules', icon: <ControlOutlined />, label: '告警规则' },
  { key: '/frp', icon: <LinkOutlined />, label: 'FRP 隧道' },
  { key: '/settings', icon: <SettingOutlined />, label: '设置' },
] as const;

function menuKey(route: string[]): string {
  const r = route[0];
  if (r === 'agent') return '/hosts';
  if (r === 'container') return '/containers';
  if (r === 'hosts') return '/hosts';
  if (r === 'containers') return '/containers';
  if (r === 'alerts') return route[1] === 'rules' ? '/alerts/rules' : '/alerts';
  if (r === 'frp') return '/frp';
  if (r === 'settings') return '/settings';
  return '/';
}

function pageTitle(route: string[]): string {
  const r = route[0];
  if (r === 'agent') return '节点详情';
  if (r === 'container') return '容器详情';
  if (r === 'hosts') return '主机';
  if (r === 'containers') return '容器';
  if (r === 'alerts') return route[1] === 'rules' ? '告警规则' : '告警事件';
  if (r === 'frp') return 'FRP 隧道';
  if (r === 'settings') return '设置';
  return '总览';
}

export default function App() {
  const route = useHashRoute();
  const authed = useStore((s) => s.authed);
  const changePwd = useStore((s) => s.changePwd);
  const user = useStore((s) => s.user);
  const agents = useStore((s) => s.agents);
  const serverOk = useStore((s) => s.serverOk);
  const logout = useStore((s) => s.logout);
  const [dark, setDark] = useState(currentTheme() === 'dark');
  const [collapsed, setCollapsed] = useState(false);

  useEffect(() => {
    if (!authed) return;
    // 刷新后从 JWT 恢复当前用户（含强制改密标记）
    useStore.getState().restoreUser();
    const on = () => {
      useStore.getState().checkHealth();
      useStore.getState().refreshAgents();
    };
    on();
    const t = setInterval(on, 15_000);
    return () => clearInterval(t);
  }, [authed]);

  const toggleTheme = () => {
    const next = dark ? 'light' : 'dark';
    applyTheme(next);
    setDark(next === 'dark');
  };

  const themeCfg = dark ? THEME_DARK : THEME_LIGHT;
  const online = agents.filter((a) => a.status === 'online').length;
  const avatarText = (user?.username ?? '?').charAt(0).toUpperCase();

  // 未登录：只渲染登录页
  if (!authed) {
    return (
      <ConfigProvider locale={zhCN} theme={themeCfg}>
        <AntApp>
          <Login />
        </AntApp>
      </ConfigProvider>
    );
  }

  // 首登/重置密码后强制改密：改完才进面板
  if (changePwd) {
    return (
      <ConfigProvider locale={zhCN} theme={themeCfg}>
        <AntApp>
          <ChangePassword />
        </AntApp>
      </ConfigProvider>
    );
  }

  let page: React.ReactNode;
  if (route[0] === 'agent' && route[1]) {
    page = <AgentDetail id={decodeURIComponent(route[1])} />;
  } else if (route[0] === 'container' && route[1] && route[2]) {
    page = <ContainerDetail agent={decodeURIComponent(route[1])} cid={decodeURIComponent(route[2])} />;
  } else if (route[0] === 'hosts') {
    page = <Hosts />;
  } else if (route[0] === 'containers') {
    page = <Containers />;
  } else if (route[0] === 'alerts' && route[1] === 'rules') {
    page = <AlertRules />;
  } else if (route[0] === 'alerts') {
    page = <Alerts />;
  } else if (route[0] === 'frp') {
    page = <Frp />;
  } else if (route[0] === 'settings') {
    page = <Settings />;
  } else {
    page = <Overview />;
  }

  return (
    <ConfigProvider locale={zhCN} theme={themeCfg}>
      <AntApp>
        <div className={`v2-shell ${collapsed ? 'collapsed' : ''}`}>
          {/* 侧栏 */}
          <aside className="v2-sidebar">
            <div className="v2-brand">
              <span className="v2-logo">L</span>
              {!collapsed && <span className="txt">litesentry</span>}
            </div>
            <nav className="v2-nav">
              {MENU.map((entry) => {
                const active = menuKey(route) === entry.key;
                return (
                  <button
                    key={entry.key}
                    onClick={() => navigate(entry.key)}
                    className={`v2-nav-item ${active ? 'active' : ''}`}
                    title={collapsed ? entry.label : undefined}
                  >
                    <span className="ico">{entry.icon}</span>
                    <span className="txt">{entry.label}</span>
                  </button>
                );
              })}
            </nav>
            <div className="v2-side-foot">
              <button className="v2-coll-btn" onClick={() => setCollapsed((c) => !c)}>
                {collapsed ? '⟩ 展开' : '⟨ 折叠'}
              </button>
            </div>
          </aside>

          {/* 顶栏 */}
          <header className="v2-topbar">
            <h1>{pageTitle(route)}</h1>
            <div className="right">
              <Tooltip title={serverOk ? '服务在线' : '服务离线'}>
                <span className="v2-status-dot">
                  <span className="v2-led" style={{ background: serverOk ? 'var(--up)' : 'var(--down)' }} />
                  {agents.length === 0 ? '连接中…' : `${online}/${agents.length} 在线`}
                </span>
              </Tooltip>
              <button
                onClick={toggleTheme}
                title="切换主题"
                className="flex h-8 w-8 items-center justify-center rounded-lg text-sm transition-colors"
                style={{ border: '1px solid var(--line)', color: 'var(--mut)', background: 'var(--panel)' }}
              >
                {dark ? <SunOutlined /> : <MoonOutlined />}
              </button>
              {user && (
                <span className="flex items-center gap-2 text-[13px]" style={{ color: 'var(--mut)' }}>
                  <span className="v2-avatar">{avatarText}</span>
                  {user.username}
                </span>
              )}
              <button onClick={logout} className="v2-back" style={{ color: 'var(--accent)' }}>
                退出
              </button>
            </div>
          </header>

          {/* 内容 */}
          <main className="v2-main">{page}</main>
        </div>
      </AntApp>
    </ConfigProvider>
  );
}
