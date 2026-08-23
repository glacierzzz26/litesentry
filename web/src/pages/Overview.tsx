// v2 总览 —— 按原型：4 张统计卡 + 主机卡片网格（CPU/内存 sparkline、磁盘/负载值）+ 最近告警表。
// 数据：overview 聚合 + 全部容器 + 最近事件 + 各在线主机 1h 序列（sparkline）。

import { useCallback, useEffect, useState } from 'react';
import { Empty, Skeleton, Table } from 'antd';
import type { TableProps } from 'antd';
import { DesktopOutlined, DeploymentUnitOutlined, WarningOutlined } from '@ant-design/icons';
import { api, hoursAgo } from '../api';
import { fmtDateTime, downsample } from '../format';
import { useStore } from '../store';
import type { AlertEvent, ContainerSample, HostSample, Overview } from '../types';
import { latestByKey } from '../types';
import type { Point } from '../components/Chart';
import { MetricLabel, SeverityTag } from '../components/ui';
import { HostCard } from '../components/HostCard';
import { StatCard } from '../components/StatCard';

export default function Overview() {
  const agents = useStore((s) => s.agents);
  const [ov, setOv] = useState<Overview | null>(null);
  const [containers, setContainers] = useState<ContainerSample[]>([]);
  const [events, setEvents] = useState<AlertEvent[]>([]);
  const [sparks, setSparks] = useState<Record<string, { cpu: Point[]; mem: Point[] }>>({});
  const [loaded, setLoaded] = useState(false);

  const load = useCallback(async () => {
    try {
      const [overview, cAll, ev] = await Promise.all([
        api.overview(hoursAgo(1)),
        api.allContainers(hoursAgo(1)),
        api.events({ limit: 8 }),
      ]);
      setOv(overview);
      setContainers([...latestByKey(cAll, (c) => c.container_id).values()]);
      setEvents(ev);

      // 每在线主机的 CPU/内存 sparkline（1h 序列降采样）
      const sp: Record<string, { cpu: Point[]; mem: Point[] }> = {};
      await Promise.all(
        overview.hosts.map(async (h) => {
          const series = await api.host(h.agent_id, hoursAgo(1));
          const t = (s: HostSample) => new Date(s.ts).getTime() / 1000;
          sp[h.agent_id] = {
            cpu: downsample(series, (s) => s.cpu_pct, t, 40) as Point[],
            mem: downsample(series, (s) => (s.mem_total ? (s.mem_used / s.mem_total) * 100 : 0), t, 40) as Point[],
          };
        }),
      );
      setSparks(sp);
      setLoaded(true);
    } catch {
      /* 网络/服务异常静默，保留旧数据 */
    }
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, 15_000);
    return () => clearInterval(t);
  }, [load]);

  const online = agents.filter((a) => a.status === 'online').length;
  const running = containers.filter((c) => c.state === 'running').length;
  const firing = ov?.firing ?? 0;

  const hostMap = new Map<string, HostSample>();
  for (const h of ov?.hosts ?? []) hostMap.set(h.agent_id, h);

  const sortedAgents = [...agents].sort((a, b) => {
    const o = (a.status === 'online' ? 0 : 1) - (b.status === 'online' ? 0 : 1);
    return o !== 0 ? o : a.hostname.localeCompare(b.hostname);
  });

  const alertColumns: TableProps<AlertEvent>['columns'] = [
    {
      title: '级别',
      key: 'sev',
      width: 90,
      render: (_: unknown, e: AlertEvent) => <SeverityTag severity={e.severity} />,
    },
    {
      title: '规则',
      dataIndex: 'rule_name',
      key: 'rule',
      ellipsis: true,
    },
    {
      title: '节点',
      key: 'node',
      width: 180,
      render: (_: unknown, e: AlertEvent) => (
        <span className="num text-xs" style={{ color: 'var(--mut)' }}>
          {e.agent_name}
          {e.entity_name && e.entity_name !== e.agent_name ? ` / ${e.entity_name}` : ''}
        </span>
      ),
    },
    {
      title: '指标',
      key: 'metric',
      width: 110,
      render: (_: unknown, e: AlertEvent) => <MetricLabel metric={e.metric} />,
    },
    {
      title: '值 / 阈值',
      key: 'val',
      width: 130,
      align: 'right',
      render: (_: unknown, e: AlertEvent) => (
        <span className="num text-xs" style={{ color: 'var(--mut)' }}>
          {e.value.toFixed(1)} / {e.threshold.toFixed(1)}
        </span>
      ),
    },
    {
      title: '开始',
      key: 'start',
      width: 130,
      render: (_: unknown, e: AlertEvent) => (
        <span className="num text-xs" style={{ color: 'var(--mut)' }}>{fmtDateTime(e.started_at)}</span>
      ),
    },
  ];

  const accentBg = 'color-mix(in srgb, var(--accent) 12%, transparent)';
  const upBg = 'color-mix(in srgb, var(--up) 14%, transparent)';
  const downBg = 'color-mix(in srgb, var(--down) 14%, transparent)';

  return (
    <div className="space-y-5">
      {/* 统计行 */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {loaded ? (
          <>
            <StatCard label="主机" value={agents.length} suffix="台" icon={<DesktopOutlined />} bg={accentBg} color="var(--accent)" to="/hosts" />
            <StatCard label="在线" value={online} suffix="台" icon={<span className="text-[10px]">●</span>} bg={upBg} color="var(--up)" to="/hosts" />
            <StatCard label="运行中容器" value={running} suffix={`/ ${containers.length}`} icon={<DeploymentUnitOutlined />} bg={accentBg} color="var(--accent)" to="/containers" />
            <StatCard label="未恢复告警" value={firing} suffix="条" icon={<WarningOutlined />} bg={downBg} color="var(--down)" to="/alerts" />
          </>
        ) : (
          Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="v2-card">
              <Skeleton active paragraph={{ rows: 1, width: '50%' }} title={{ width: '40%' }} />
            </div>
          ))
        )}
      </div>

      {/* 主机卡片 */}
      <div>
        <div className="v2-section-title">主机</div>
        {sortedAgents.length === 0 ? (
          <div className="v2-card">
            <Empty description="暂无 Agent 上报。先部署 litesentry-agent 并配置 LS_SERVER。" />
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
            {sortedAgents.map((a) => (
              <HostCard
                key={a.agent_id}
                agent={a}
                host={hostMap.get(a.agent_id)}
                diskMax={ov?.disk_max[a.agent_id]}
                cpuSpark={sparks[a.agent_id]?.cpu}
                memSpark={sparks[a.agent_id]?.mem}
              />
            ))}
          </div>
        )}
      </div>

      {/* 最近告警 */}
      <div>
        <div className="v2-section-title">最近告警</div>
        {events.length === 0 ? (
          <div className="v2-card">
            <Empty description="暂无告警事件" image={Empty.PRESENTED_IMAGE_SIMPLE} />
          </div>
        ) : (
          <div className="v2-table-card">
            <Table<AlertEvent>
              rowKey="id"
              size="small"
              dataSource={events}
              pagination={false}
              columns={alertColumns}
            />
          </div>
        )}
      </div>
    </div>
  );
}
