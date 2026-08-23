// 展示层格式化工具。

export function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

export function fmtRate(n: number): string {
  return fmtBytes(n) + '/s';
}

export function fmtPct(v: number): string {
  if (!Number.isFinite(v)) return '—';
  return `${v.toFixed(1)}%`;
}

export function fmtUptime(s: number): string {
  if (!Number.isFinite(s) || s <= 0) return '—';
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m ${Math.floor(s % 60)}s`;
}

/** 指标数值统一配色（主机/容器/磁盘等百分比进度条共用）：
 *    ≤60 绿（正常）· 60–85 琥珀（warning）· >85 红（critical） */
export function metricColor(v: number): string {
  return v > 85 ? 'var(--down)' : v > 60 ? 'var(--amber)' : 'var(--up)';
}

export function timeAgo(iso: string): string {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return '—';
  const diff = Date.now() - t;
  if (diff < 0) return '刚刚';
  const s = Math.floor(diff / 1000);
  if (s < 60) return `${s}s 前`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} 分钟前`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h} 小时前`;
  return `${Math.floor(h / 24)} 天前`;
}

/** RFC3339 → 本地 HH:MM:SS */
export function fmtClock(iso: string): string {
  const d = new Date(iso);
  if (!Number.isFinite(d.getTime())) return iso;
  const p = (x: number) => String(x).padStart(2, '0');
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

/** RFC3339 → 本地 MM-DD HH:mm（列表/事件时间列） */
export function fmtDateTime(iso: string): string {
  const d = new Date(iso);
  if (!Number.isFinite(d.getTime())) return iso;
  const p = (x: number) => String(x).padStart(2, '0');
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** unix 秒 → HH:MM:SS（图表横轴） */
export function fmtClockSec(unixSec: number): string {
  const d = new Date(unixSec * 1000);
  const p = (x: number) => String(x).padStart(2, '0');
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

/** 将时序数据降采样到 maxPoints 个点（保留首尾），避免大窗口图表过密。 */
export function downsample<T>(rows: T[], value: (r: T) => number, time: (r: T) => number, maxPoints = 120): { time: number; value: number }[] {
  if (rows.length === 0) return [];
  if (rows.length <= maxPoints) {
    return rows.map((r) => ({ time: time(r), value: value(r) }));
  }
  const step = (rows.length - 1) / (maxPoints - 1);
  const out: { time: number; value: number }[] = [];
  for (let i = 0; i < maxPoints; i++) {
    const idx = Math.round(i * step);
    out.push({ time: time(rows[idx]), value: value(rows[idx]) });
  }
  return out;
}

