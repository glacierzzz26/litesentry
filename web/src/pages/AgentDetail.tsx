// v2 节点详情 —— 按原型：返回 + 名称 + LED + 时间窗，CPU/内存/网络/负载四图，
// 磁盘占用 + Agent 自身开销，容器表 + 地址表（IPv4/IPv6，复制）。
// 离线节点：图表显示「节点离线」，地址仍读注册信息。

import { useCallback, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import { Empty, Segmented, Skeleton, Table } from 'antd';
import { api, hoursAgo } from '../api';
import { fmtBytes, downsample, metricColor } from '../format';
import { navigate } from '../route';
import { useStore } from '../store';
import type { ContainerSample, DiskSample, HostSample } from '../types';
import { latestByKey } from '../types';
import type { Point } from '../components/Chart';
import { MetricChart } from '../components/Chart';
import { ContainerStateTag } from '../components/ui';
import { Led } from '../components/Led';
import { CopyBtn } from '../components/CopyBtn';

const WINDOWS = [
  { label: '1h', value: 1 },
  { label: '6h', value: 6 },
  { label: '24h', value: 24 },
  { label: '7d', value: 168 },
];

function ChartCard({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="v2-card">
      <h3>{title}</h3>
      {children}
    </div>
  );
}

interface AddrRow {
  key: string;
  iface: string;
  family: 'IPv4' | 'IPv6';
  addr: string;
}

export default function AgentDetail({ id }: { id: string }) {
  const agents = useStore((s) => s.agents);
  const agent = useMemo(() => agents.find((a) => a.agent_id === id), [agents, id]);

  const [win, setWin] = useState(1);
  const [hosts, setHosts] = useState<HostSample[]>([]);
  const [disks, setDisks] = useState<DiskSample[]>([]);
  const [containers, setContainers] = useState<ContainerSample[]>([]);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [h, d, c] = await Promise.all([
        api.host(id, hoursAgo(win)),
        api.disks(id, hoursAgo(win)),
        api.containers(id, hoursAgo(win)),
      ]);
      setHosts(h);
      setDisks(d);
      setContainers(c);
    } catch {
      /* 网络/服务异常静默，保留旧数据 */
    } finally {
      setLoading(false);
    }
  }, [id, win]);

  useEffect(() => {
    load();
    const t = setInterval(load, 15_000);
    return () => clearInterval(t);
  }, [load]);

  const last = hosts[hosts.length - 1];
  const name = agent?.hostname ?? last?.hostname ?? '未知节点';
  const led = agent?.status === 'online' ? 'on' : 'off';

  const chartData = useMemo(() => {
    const t = (s: HostSample) => new Date(s.ts).getTime() / 1000;
    return {
      cpu: downsample(hosts, (s) => s.cpu_pct, t, 200) as Point[],
      mem: downsample(hosts, (s) => (s.mem_total ? (s.mem_used / s.mem_total) * 100 : 0), t, 200) as Point[],
      load1: downsample(hosts, (s) => s.load_1m, t, 200) as Point[],
      rx: downsample(hosts, (s) => s.net_rx_bps / 1e6, t, 200) as Point[],
      tx: downsample(hosts, (s) => s.net_tx_bps / 1e6, t, 200) as Point[],
    };
  }, [hosts]);

  const diskLatest = useMemo(
    () =>
      [...latestByKey(disks, (d) => d.mount).values()].sort(
        (a, b) => b.used / (b.total || 1) - a.used / (a.total || 1),
      ),
    [disks],
  );

  const cLatest = useMemo(
    () =>
      [...latestByKey(containers, (c) => c.container_id).values()].sort((a, b) => b.cpu_pct - a.cpu_pct),
    [containers],
  );

  const addrs = useMemo<AddrRow[]>(() => {
    const out: AddrRow[] = [];
    for (const i of agent?.ipv4 ?? []) out.push({ key: `v4-${out.length}`, iface: i.iface, family: 'IPv4', addr: i.addr });
    for (const i of agent?.ipv6 ?? []) out.push({ key: `v6-${out.length}`, iface: i.iface, family: 'IPv6', addr: i.addr });
    return out;
  }, [agent]);

  const noData = hosts.length === 0;

  if (loading && hosts.length === 0) {
    return (
      <div className="space-y-4">
        <Skeleton active paragraph={{ rows: 1 }} />
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="v2-card">
              <Skeleton active paragraph={{ rows: 3 }} />
            </div>
          ))}
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-5">
      {/* 页头：返回 + 名称 + LED + 时间窗 */}
      <div className="v2-page-head flex-wrap">
        <button className="v2-back" onClick={() => navigate('/hosts')}>← 主机</button>
        <h2>{name}</h2>
        <Led state={led} />
        <div style={{ marginLeft: 'auto' }}>
          <Segmented options={WINDOWS} value={win} onChange={(v) => setWin(v as number)} />
        </div>
      </div>

      {noData ? (
        <>
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            {['CPU 使用率 %', '内存使用 %', '网络 入/出 (Mbps)', '系统负载 1m'].map((t) => (
              <div key={t} className="v2-card">
                <h3>{t}</h3>
                <div className="py-12 text-center text-sm" style={{ color: 'var(--mut)' }}>
                  节点离线
                </div>
              </div>
            ))}
          </div>
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <div className="v2-card">
              <h3>磁盘占用</h3>
              <span style={{ color: 'var(--faint)' }}>—</span>
            </div>
            <div className="v2-card">
              <h3>Agent 自身开销</h3>
              <div className="v2-kv">
                <span className="k">状态</span><span>离线</span>
              </div>
            </div>
          </div>
        </>
      ) : (
        <>
          {/* 曲线 */}
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <ChartCard title="CPU 使用率 %">
              <MetricChart series={[{ label: 'CPU', color: 'var(--cat-1)', data: chartData.cpu }]} height={200} unit="%" />
            </ChartCard>
            <ChartCard title="内存使用 %">
              <MetricChart series={[{ label: '内存', color: 'var(--cat-3)', data: chartData.mem }]} height={200} unit="%" />
            </ChartCard>
            <ChartCard title="网络 入/出 (Mbps)">
              <MetricChart
                series={[
                  { label: '↓ 下载', color: 'var(--cat-3)', data: chartData.rx },
                  { label: '↑ 上传', color: 'var(--cat-2)', data: chartData.tx },
                ]}
                height={200}
              />
            </ChartCard>
            <ChartCard title="系统负载 1m">
              <MetricChart series={[{ label: '1m', color: 'var(--cat-7)', data: chartData.load1 }]} height={200} />
            </ChartCard>
          </div>

          {/* 磁盘 + Agent 自身开销 */}
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <div className="v2-card">
              <h3>磁盘占用</h3>
              {diskLatest.length === 0 ? (
                <span style={{ color: 'var(--faint)' }}>—</span>
              ) : (
                <div className="space-y-3">
                  {diskLatest.map((d) => {
                    const pct = d.total ? (d.used / d.total) * 100 : 0;
                    return (
                      <div key={d.mount} className="grid grid-cols-[110px_70px_1fr_70px] items-center gap-3">
                        <span className="num truncate text-sm" style={{ color: 'var(--fg)' }}>{d.mount}</span>
                        <span className="text-xs" style={{ color: 'var(--faint)' }}>{d.fs}</span>
                        <div className="mini-track">
                          <div className="mini-fill" style={{ width: `${Math.min(100, pct)}%`, background: metricColor(pct) }} />
                        </div>
                        <span className="num text-right text-xs" style={{ color: 'var(--mut)' }}>
                          {pct.toFixed(0)}%{pct > 85 ? <span style={{ color: 'var(--down)' }}> · 高</span> : ''}
                        </span>
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
            <div className="v2-card">
              <h3>Agent 自身开销</h3>
              <div className="v2-kv">
                <span className="k">Agent CPU</span><span className="num">{last.agent_cpu_pct.toFixed(1)}%</span>
                <span className="k">Agent RSS</span><span className="num">{fmtBytes(last.agent_mem_rss)}</span>
                <span className="k">上报间隔</span><span className="num">60s</span>
                <span className="k">版本</span><span className="num">{agent?.version || '—'}</span>
              </div>
            </div>
          </div>
        </>
      )}

      {/* 容器 */}
      <div>
        <div className="v2-section-title">容器</div>
        <div className="v2-table-card">
          <Table<ContainerSample>
            rowKey={(c) => c.container_id}
            size="small"
            dataSource={cLatest}
            pagination={false}
            locale={{ emptyText: '无运行容器' }}
            columns={[
              {
                title: '名称',
                key: 'name',
                width: 200,
                ellipsis: true,
                render: (_: unknown, c: ContainerSample) => (
                  <a
                    onClick={(e) => { e.stopPropagation(); navigate(`/container/${encodeURIComponent(c.agent_id)}/${encodeURIComponent(c.container_id)}`); }}
                    style={{ color: 'var(--accent)' }}
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
            ]}
          />
        </div>
      </div>

      {/* 地址 */}
      <div>
        <div className="v2-section-title">地址（IPv4 / IPv6）</div>
        <div className="v2-table-card">
          <Table<AddrRow>
            rowKey="key"
            size="small"
            dataSource={addrs}
            pagination={false}
            locale={{ emptyText: '无地址数据' }}
            columns={[
              {
                title: '网卡',
                dataIndex: 'iface',
                key: 'iface',
                width: 160,
                render: (v: string) => <span className="num text-sm">{v}</span>,
              },
              {
                title: '类型',
                dataIndex: 'family',
                key: 'family',
                width: 100,
                render: (v: 'IPv4' | 'IPv6') => <span className={`v2-tag ${v === 'IPv6' ? 'run' : 'ok'}`}>{v}</span>,
              },
              {
                title: '地址',
                dataIndex: 'addr',
                key: 'addr',
                render: (v: string) => <span className="num text-xs" style={{ color: 'var(--mut)' }}>{v}</span>,
              },
              {
                title: '',
                key: 'op',
                width: 80,
                render: (_: unknown, r: AddrRow) => <CopyBtn text={r.addr} />,
              },
            ]}
          />
        </div>
      </div>

      {/* 无数据兜底 */}
      {noData && (
        <div className="v2-card">
          <Empty description="节点离线或无历史数据，确认 Agent 已上线并完成上报。" image={Empty.PRESENTED_IMAGE_SIMPLE} />
        </div>
      )}
    </div>
  );
}
