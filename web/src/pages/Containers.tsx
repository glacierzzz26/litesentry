// v2 容器列表（跨节点）—— 按原型：节点 + 名称 + 镜像 + 状态 + 重启 + CPU + 内存 + 网络。
// 数据：allContainers（按 agents 扇出取每容器最近采样）+ agents 主机名映射。

import { useCallback, useEffect, useMemo, useState } from 'react';
import { Skeleton, Table } from 'antd';
import { api, hoursAgo } from '../api';
import { fmtBytes, fmtUptime } from '../format';
import { navigate } from '../route';
import { useStore } from '../store';
import type { ContainerSample } from '../types';
import { ContainerStateTag } from '../components/ui';

export default function Containers() {
  const agents = useStore((s) => s.agents);
  const [rows, setRows] = useState<ContainerSample[]>([]);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setRows(await api.allContainers(hoursAgo(1)));
    } catch {
      /* 网络/服务异常静默，保留旧数据 */
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, 15_000);
    return () => clearInterval(t);
  }, [load]);

  const hostname = useMemo(() => {
    const m = new Map<string, string>();
    for (const a of agents) m.set(a.agent_id, a.hostname);
    return m;
  }, [agents]);

  const sorted = useMemo(() => [...rows].sort((a, b) => b.cpu_pct - a.cpu_pct), [rows]);

  if (loading && rows.length === 0) {
    return (
      <div className="v2-card">
        <Skeleton active paragraph={{ rows: 6 }} />
      </div>
    );
  }

  return (
    <div className="v2-table-card">
      <Table<ContainerSample>
        rowKey={(c) => `${c.agent_id}/${c.container_id}`}
        size="middle"
        dataSource={sorted}
        pagination={false}
        scroll={{ x: 1180 }}
        locale={{ emptyText: '暂无容器数据（Agent 未接入 Docker 或无容器运行）。' }}
        columns={[
          {
            title: '节点',
            key: 'host',
            width: 150,
            render: (_: unknown, c: ContainerSample) => (
              <a
                onClick={(e) => { e.stopPropagation(); navigate(`/agent/${encodeURIComponent(c.agent_id)}`); }}
                style={{ color: 'var(--accent)' }}
              >
                {hostname.get(c.agent_id) ?? '未知节点'}
              </a>
            ),
          },
          {
            title: '名称',
            key: 'name',
            width: 200,
            ellipsis: true,
            render: (_: unknown, c: ContainerSample) => (
              <a
                className="font-medium"
                onClick={(e) => { e.stopPropagation(); navigate(`/container/${encodeURIComponent(c.agent_id)}/${encodeURIComponent(c.container_id)}`); }}
                style={{ color: 'var(--fg)' }}
              >
                {c.name || '(unnamed)'}
              </a>
            ),
          },
          {
            title: '镜像',
            dataIndex: 'image',
            key: 'image',
            ellipsis: true,
            render: (v: string) => <span className="num text-xs" style={{ color: 'var(--faint)' }}>{v || '—'}</span>,
          },
          {
            title: '状态',
            dataIndex: 'state',
            key: 'state',
            width: 110,
            render: (v: string) => <ContainerStateTag state={v} />,
          },
          {
            title: '重启',
            dataIndex: 'restarts',
            key: 'restarts',
            width: 70,
            align: 'right',
            render: (v: number) => <span className="num">{v}</span>,
          },
          {
            title: 'CPU',
            dataIndex: 'cpu_pct',
            key: 'cpu',
            width: 90,
            align: 'right',
            render: (v: number) => <span className="num">{v.toFixed(1)}%</span>,
          },
          {
            title: '内存',
            key: 'mem',
            width: 110,
            align: 'right',
            render: (_: unknown, c: ContainerSample) => (
              <span className="num text-xs" style={{ color: 'var(--mut)' }}>{fmtBytes(c.mem_usage)}</span>
            ),
          },
          {
            title: '网络 ↑/↓',
            key: 'net',
            width: 150,
            align: 'right',
            render: (_: unknown, c: ContainerSample) => (
              <span className="num text-xs" style={{ color: 'var(--mut)' }}>
                {fmtBytes(c.net_rx_bps)} / {fmtBytes(c.net_tx_bps)}
              </span>
            ),
          },
          {
            title: '运行',
            dataIndex: 'uptime_s',
            key: 'uptime',
            width: 90,
            align: 'right',
            render: (v: number) => (
              <span className="num text-xs" style={{ color: 'var(--mut)' }}>{fmtUptime(v)}</span>
            ),
          },
        ]}
      />
    </div>
  );
}
