// v2 容器详情 —— 按原型：返回 + 名称 + 状态标签，节点/镜像/重启键值行，CPU % + 内存 两张曲线。
// 数据：containers(agent, window) 按 container_id 过滤。

import { useCallback, useEffect, useMemo, useState } from 'react';
import { Empty, Segmented, Skeleton } from 'antd';
import { api, hoursAgo } from '../api';
import { fmtBytes, fmtUptime, downsample } from '../format';
import { navigate } from '../route';
import { useStore } from '../store';
import type { ContainerSample } from '../types';
import type { Point } from '../components/Chart';
import { MetricChart } from '../components/Chart';
import { ContainerStateTag } from '../components/ui';

const WINDOWS = [
  { label: '1h', value: 1 },
  { label: '6h', value: 6 },
  { label: '24h', value: 24 },
  { label: '7d', value: 168 },
];

export default function ContainerDetail({ agent, cid }: { agent: string; cid: string }) {
  const agents = useStore((s) => s.agents);
  const hostname = useMemo(
    () => agents.find((a) => a.agent_id === agent)?.hostname ?? '未知节点',
    [agents, agent],
  );

  const [win, setWin] = useState(1);
  const [rows, setRows] = useState<ContainerSample[]>([]);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const all = await api.containers(agent, hoursAgo(win));
      setRows(all.filter((c) => c.container_id === cid));
    } catch {
      /* 网络/服务异常静默，保留旧数据 */
    } finally {
      setLoading(false);
    }
  }, [agent, cid, win]);

  useEffect(() => {
    load();
    const t = setInterval(load, 15_000);
    return () => clearInterval(t);
  }, [load]);

  const last = rows[rows.length - 1];
  const name = last?.name || '(unnamed)';

  const chart = useMemo(() => {
    const t = (r: ContainerSample) => new Date(r.ts).getTime() / 1000;
    return {
      cpu: downsample(rows, (r) => r.cpu_pct, t, 200) as Point[],
      mem: downsample(rows, (r) => (r.mem_limit ? (r.mem_usage / r.mem_limit) * 100 : 0), t, 200) as Point[],
    };
  }, [rows]);

  if (loading && rows.length === 0) {
    return (
      <div className="space-y-4">
        <Skeleton active paragraph={{ rows: 1 }} />
        <div className="v2-card"><Skeleton active paragraph={{ rows: 2 }} /></div>
      </div>
    );
  }

  return (
    <div className="space-y-5">
      {/* 页头 */}
      <div className="v2-page-head flex-wrap">
        <button className="v2-back" onClick={() => navigate('/containers')}>← 容器</button>
        <h2>{name}</h2>
        {last && <ContainerStateTag state={last.state} />}
        <div style={{ marginLeft: 'auto' }}>
          <Segmented options={WINDOWS} value={win} onChange={(v) => setWin(v as number)} />
        </div>
      </div>

      {rows.length === 0 ? (
        <div className="v2-card">
          <Empty description="该容器暂无数据（可能已删除，或 Agent 未采集到）。" />
        </div>
      ) : (
        <>
          {/* 基本信息键值行 */}
          <div className="v2-card">
            <div className="v2-kv" style={{ gridTemplateColumns: 'auto 1fr auto 1fr auto 1fr' }}>
              <span className="k">节点</span>
              <a className="num" style={{ color: 'var(--accent)' }} onClick={() => navigate(`/agent/${encodeURIComponent(agent)}`)}>
                {hostname}
              </a>
              <span className="k">镜像</span>
              <span className="num" style={{ color: 'var(--fg)' }}>{last.image || '—'}</span>
              <span className="k">重启</span>
              <span className="num" style={{ color: 'var(--fg)' }}>{last.restarts}</span>
            </div>
          </div>

          {/* 曲线 */}
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <div className="v2-card">
              <h3>CPU %</h3>
              <MetricChart series={[{ label: 'CPU', color: 'var(--cat-1)', data: chart.cpu }]} height={200} unit="%" />
            </div>
            <div className="v2-card">
              <h3>内存</h3>
              <MetricChart series={[{ label: '内存', color: 'var(--cat-3)', data: chart.mem }]} height={200} unit="%" />
            </div>
          </div>

          {/* 补充信息 */}
          <div className="v2-card">
            <div className="v2-kv">
              <span className="k">内存占用</span><span className="num">{fmtBytes(last.mem_usage)}</span>
              <span className="k">内存限制</span><span className="num">{last.mem_limit ? fmtBytes(last.mem_limit) : '无'}</span>
              <span className="k">网络 ↓/↑</span><span className="num">{fmtBytes(last.net_rx_bps)} / {fmtBytes(last.net_tx_bps)}</span>
              <span className="k">运行时长</span><span className="num">{fmtUptime(last.uptime_s)}</span>
            </div>
          </div>
        </>
      )}
    </div>
  );
}
