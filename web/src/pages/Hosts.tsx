// v2 主机列表 —— 按原型：状态 LED + 主机（链接）+ 系统 + 版本 + CPU/内存/磁盘（进度条）+ 负载 + Agent 开销。
// 数据：store 的 agents + overview 聚合（每节点最近样本 / 磁盘峰值）。

import { useCallback, useEffect, useState } from 'react';
import { Table } from 'antd';
import { api, hoursAgo } from '../api';
import { fmtBytes, metricColor } from '../format';
import { navigate } from '../route';
import { useStore } from '../store';
import type { Agent, HostSample, Overview } from '../types';
import { Led } from '../components/Led';

/** 表格单元格：数字 + 迷你进度条（同原型 .cell）。 */
function CellBar({ value }: { value?: number }) {
  if (value === undefined) return <span style={{ color: 'var(--faint)' }}>—</span>;
  const c = metricColor(value);
  return (
    <div className="flex items-center gap-2">
      <span className="num text-sm font-semibold" style={{ color: c, minWidth: 44 }}>
        {value.toFixed(0)}%
      </span>
      <div className="mini-track" style={{ flex: 1, minWidth: 70 }}>
        <div className="mini-fill" style={{ width: `${Math.min(100, value)}%`, background: c }} />
      </div>
    </div>
  );
}

export default function Hosts() {
  const agents = useStore((s) => s.agents);
  const agentsLoading = useStore((s) => s.agentsLoading);
  const [latest, setLatest] = useState<Record<string, HostSample>>({});
  const [diskMax, setDiskMax] = useState<Record<string, number>>({});

  const load = useCallback(async () => {
    try {
      const ov: Overview = await api.overview(hoursAgo(1));
      const hm: Record<string, HostSample> = {};
      for (const h of ov.hosts) hm[h.agent_id] = h;
      setLatest(hm);
      setDiskMax(ov.disk_max);
    } catch {
      /* 网络/服务异常静默，保留旧数据 */
    }
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, 15_000);
    return () => clearInterval(t);
  }, [load]);

  const rows = [...agents].sort((a, b) => {
    const o = (a.status === 'online' ? 0 : 1) - (b.status === 'online' ? 0 : 1);
    return o !== 0 ? o : a.hostname.localeCompare(b.hostname);
  });

  return (
    <div className="v2-table-card">
      <Table<Agent>
        rowKey="agent_id"
        size="middle"
        loading={agentsLoading && agents.length === 0}
        dataSource={rows}
        pagination={false}
        scroll={{ x: 1180 }}
        locale={{ emptyText: '暂无 Agent 上报。先部署 litesentry-agent 并配置 LS_SERVER。' }}
        columns={[
          {
            title: '状态',
            key: 'led',
            width: 56,
            render: (_: unknown, a: Agent) => (
              <Led state={a.status === 'online' ? 'on' : 'off'} />
            ),
          },
          {
            title: '主机',
            key: 'host',
            width: 180,
            ellipsis: true,
            render: (_: unknown, a: Agent) => (
              <a
                className="font-medium"
                onClick={(e) => { e.stopPropagation(); navigate(`/agent/${a.agent_id}`); }}
                style={{ color: 'var(--accent)' }}
              >
                {a.hostname}
              </a>
            ),
          },
          {
            title: '系统',
            key: 'os',
            width: 200,
            render: (_: unknown, a: Agent) => (
              <span className="text-xs" style={{ color: 'var(--mut)' }}>
                {a.os} · {a.arch}
              </span>
            ),
          },
          {
            title: '版本',
            key: 'version',
            width: 80,
            render: (_: unknown, a: Agent) => (
              <span className="num text-xs" style={{ color: a.status === 'online' ? 'var(--mut)' : 'var(--faint)' }}>
                {a.status === 'online' ? (a.version || '—') : '—'}
              </span>
            ),
          },
          {
            title: 'CPU',
            key: 'cpu',
            width: 170,
            render: (_: unknown, a: Agent) => (
              <CellBar value={latest[a.agent_id]?.cpu_pct} />
            ),
          },
          {
            title: '内存',
            key: 'mem',
            width: 170,
            render: (_: unknown, a: Agent) => {
              const h = latest[a.agent_id];
              return <CellBar value={h && h.mem_total ? (h.mem_used / h.mem_total) * 100 : undefined} />;
            },
          },
          {
            title: '磁盘',
            key: 'disk',
            width: 170,
            render: (_: unknown, a: Agent) => <CellBar value={diskMax[a.agent_id]} />,
          },
          {
            title: '负载',
            key: 'load',
            width: 90,
            align: 'right',
            render: (_: unknown, a: Agent) => {
              const h = latest[a.agent_id];
              return <span className="num">{h ? h.load_1m.toFixed(2) : '—'}</span>;
            },
          },
          {
            title: 'Agent 开销',
            key: 'self',
            width: 140,
            align: 'right',
            render: (_: unknown, a: Agent) => {
              const h = latest[a.agent_id];
              return (
                <span className="num text-xs" style={{ color: 'var(--mut)' }}>
                  {h ? `${h.agent_cpu_pct.toFixed(1)}% / ${fmtBytes(h.agent_mem_rss)}` : '—'}
                </span>
              );
            },
          },
        ]}
        onRow={(a) => ({ onClick: () => navigate(`/agent/${a.agent_id}`), style: { cursor: 'pointer' } })}
      />
    </div>
  );
}
