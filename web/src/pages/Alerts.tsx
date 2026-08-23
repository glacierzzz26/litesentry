// v2 告警事件 —— 按原型：持续告警横幅 + 事件表（级别/规则/节点实体/指标/值阈值/状态/开始）。
// 数据：events（触发中在前，按开始时间倒序）。

import { useCallback, useEffect, useMemo, useState } from 'react';
import { Empty, Skeleton, Table } from 'antd';
import { api } from '../api';
import { fmtDateTime } from '../format';
import type { AlertEvent } from '../types';
import { EventStateTag, MetricLabel, SeverityTag } from '../components/ui';

export default function Alerts() {
  const [events, setEvents] = useState<AlertEvent[]>([]);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setEvents(await api.events({ limit: 200 }));
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

  const sorted = useMemo(
    () =>
      [...events].sort((a, b) => {
        if (a.state !== b.state) return a.state === 'firing' ? -1 : 1;
        return new Date(b.started_at).getTime() - new Date(a.started_at).getTime();
      }),
    [events],
  );

  const firing = events.filter((e) => e.state === 'firing');
  const affected = [...new Set(firing.map((e) => e.agent_name).filter(Boolean))];

  if (loading && events.length === 0) {
    return (
      <div className="v2-card">
        <Skeleton active paragraph={{ rows: 6 }} />
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {firing.length > 0 && (
        <div className="v2-alert-bar firing">
          <span style={{ fontSize: 10 }}>●</span>
          {firing.length} 条告警持续中，请关注 {affected.join(' / ')}
        </div>
      )}

      {sorted.length === 0 ? (
        <div className="v2-card">
          <Empty description="暂无告警事件" image={Empty.PRESENTED_IMAGE_SIMPLE} />
        </div>
      ) : (
        <div className="v2-table-card">
          <Table<AlertEvent>
            rowKey="id"
            size="middle"
            dataSource={sorted}
            pagination={false}
            scroll={{ x: 1100 }}
            columns={[
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
                render: (v: string) => <span className="font-medium" style={{ color: 'var(--fg)' }}>{v}</span>,
              },
              {
                title: '节点 / 实体',
                key: 'node',
                width: 200,
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
                title: '状态',
                dataIndex: 'state',
                key: 'state',
                width: 100,
                render: (v: string) => <EventStateTag state={v} />,
              },
              {
                title: '开始',
                key: 'start',
                width: 140,
                render: (_: unknown, e: AlertEvent) => (
                  <span className="num text-xs" style={{ color: 'var(--mut)' }}>{fmtDateTime(e.started_at)}</span>
                ),
              },
            ]}
          />
        </div>
      )}
    </div>
  );
}
