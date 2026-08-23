// v2 主机卡 —— 按原型：主机名 + LED + 状态标签 + 系统/架构副行，
// CPU/内存（值 + sparkline）/ 磁盘 / 负载 四格指标，整卡可点击进详情。
// 数据走真实 API（复用 store 的 agents + overview 的 host 样本）。

import type { Agent, HostSample } from '../types';
import type { Point } from '../components/Chart';
import { Sparkline } from '../components/Chart';
import { metricColor } from '../format';
import { navigate } from '../route';
import { Led } from './Led';
import type { LedState } from './Led';

export function HostCard({ agent, host, diskMax, cpuSpark, memSpark }: {
  agent: Agent;
  host?: HostSample;
  diskMax?: number;
  cpuSpark?: Point[];
  memSpark?: Point[];
}) {
  const online = agent.status === 'online';
  const cpu = host ? host.cpu_pct : 0;
  const memPct = host && host.mem_total ? (host.mem_used / host.mem_total) * 100 : 0;
  const disk = diskMax ?? 0;
  const load = host ? host.load_1m : 0;

  // 在线但指标告警 → 高负载；否则正常；离线直接灰
  const warn = online && (cpu >= 90 || disk >= 90);
  const led: LedState = !online ? 'off' : warn ? 'warn' : 'on';
  const tag = !online ? { cls: 'down', text: '离线' } : warn ? { cls: 'warn', text: '高负载' } : { cls: 'ok', text: '正常' };

  const cpuColor = metricColor(cpu);
  const memColor = metricColor(memPct);

  return (
    <button className="v2-hostcard" onClick={() => navigate(`/agent/${agent.agent_id}`)}>
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="node-name">
            <Led state={led} />
            <span className="truncate">{agent.hostname}</span>
          </div>
          <div className="node-sub">
            {agent.os} · {agent.arch}
          </div>
        </div>
        <span className={`v2-tag ${tag.cls}`}>{tag.text}</span>
      </div>

      <div className="v2-metrics">
        <div className="v2-metric">
          <div className="lab">CPU</div>
          <div className="val num" style={{ color: cpuColor }}>{online ? `${cpu.toFixed(0)}%` : '—'}</div>
          {online && cpuSpark && cpuSpark.length > 1 && (
            <div style={{ color: cpuColor }}><Sparkline data={cpuSpark} color={cpuColor} height={30} /></div>
          )}
        </div>
        <div className="v2-metric">
          <div className="lab">内存</div>
          <div className="val num" style={{ color: memColor }}>{online ? `${memPct.toFixed(0)}%` : '—'}</div>
          {online && memSpark && memSpark.length > 1 && (
            <div style={{ color: memColor }}><Sparkline data={memSpark} color={memColor} height={30} /></div>
          )}
        </div>
        <div className="v2-metric">
          <div className="lab">磁盘</div>
          <div className="val num">{online ? `${disk.toFixed(0)}%` : '—'}</div>
        </div>
        <div className="v2-metric">
          <div className="lab">负载</div>
          <div className="val num">{online ? load.toFixed(2) : '—'}</div>
        </div>
      </div>
    </button>
  );
}
