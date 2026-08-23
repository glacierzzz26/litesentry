// 复用小组件：状态/容器状态/级别 Tag，指标标签。
// 视觉对齐 Beszel/Uptime Kuma：状态优先用圆点 + 文字，颜色不单独承担含义。

import type { ReactNode } from 'react';
import { METRIC_LABELS } from '../constants';

// 节点状态 → 视觉。服务端权威判定 status。
const STATUS_META: Record<string, { label: string; dot: string }> = {
  online: { label: '在线', dot: 'dot-up' },
  offline: { label: '离线', dot: 'dot-down' },
  upgrading: { label: '升级中', dot: '' },
};

/** 节点在线状态：圆点 + 文字。 */
export function StatusTag({ status }: { status?: string }) {
  const meta = status ? STATUS_META[status] : undefined;
  return (
    <span className="inline-flex items-center gap-1.5 text-xs" style={{ color: 'var(--mut)' }}>
      <span className={`dot ${meta?.dot ?? ''}`} style={!meta?.dot ? { background: 'var(--faint)' } : undefined} />
      {meta?.label ?? '未知'}
    </span>
  );
}

const CONTAINER_STATE: Record<string, { label: string; color: string }> = {
  running: { label: '运行中', color: 'var(--up)' },
  exited: { label: '已退出', color: 'var(--faint)' },
  dead: { label: '已死亡', color: 'var(--down)' },
  paused: { label: '已暂停', color: 'var(--amber)' },
};

/** 容器状态：小圆点 + 文字（颜色 + 标签双重编码，非纯色）。 */
export function ContainerStateTag({ state }: { state?: string }) {
  const meta = state ? CONTAINER_STATE[state] : undefined;
  return (
    <span className="inline-flex items-center gap-1.5 text-xs">
      <span className="dot" style={{ background: meta?.color ?? 'var(--faint)' }} />
      <span style={{ color: 'var(--fg)' }}>{meta?.label ?? state ?? '—'}</span>
    </span>
  );
}

/** 告警级别：色块徽标 + 文字。 */
export function SeverityTag({ severity }: { severity?: string }) {
  if (severity === 'critical') return <Badge color="var(--down)" bg="color-mix(in srgb, var(--down) 12%, transparent)">严重</Badge>;
  if (severity === 'warning') return <Badge color="var(--amber)" bg="color-mix(in srgb, var(--amber) 14%, transparent)">警告</Badge>;
  return <Badge color="var(--mut)" bg="var(--panel2)">—</Badge>;
}

/** 告警事件状态：firing/resolved 徽标。 */
export function EventStateTag({ state }: { state?: string }) {
  if (state === 'resolved') return <Badge color="var(--up)" bg="color-mix(in srgb, var(--up) 12%, transparent)">已恢复</Badge>;
  if (state === 'firing') return <Badge color="var(--down)" bg="color-mix(in srgb, var(--down) 12%, transparent)">触发中</Badge>;
  return <Badge color="var(--mut)" bg="var(--panel2)">—</Badge>;
}

function Badge({ children, color, bg }: { children: ReactNode; color: string; bg: string }) {
  return (
    <span
      className="inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium"
      style={{ color, background: bg }}
    >
      {children}
    </span>
  );
}

/** 指标 → 中文名。 */
export function MetricLabel({ metric }: { metric?: string }) {
  return <>{METRIC_LABELS[metric ?? ''] ?? metric ?? '—'}</>;
}
