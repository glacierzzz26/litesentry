// 手写 SVG 图表组件 —— 遵循 dataviz 方法：
//   · 2px 线、面积填充 ~10% 透明（wash，非饱和块）
//   · 端点圆点带 2px 表面色环（surface ring）
//   · 网格线 hairline 1px、recessive
//   · 文字穿 text token，不穿序列色
//   · 十字线 tooltip：竖线吸附最近 X，列出该 X 下全部序列
//   · 单序列无图例框（标题即说明）；多序列配图例
// 不引 @ant-design/charts，体积小、视觉完全可控。

import { useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { metricColor } from '../format';

export interface Point {
  time: number; // unix 秒
  value: number;
}

export interface SeriesDef {
  label: string;
  color: string; // CSS 变量或 hex
  data: Point[];
}

const fmtMs = (ms: number): string => {
  const d = new Date(ms);
  const p = (x: number) => String(x).padStart(2, '0');
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
};

const fmtVal = (v: number): string => {
  if (!Number.isFinite(v)) return '—';
  const a = Math.abs(v);
  if (a >= 1e4) return (v / 1e3).toFixed(1) + 'k';
  return v.toFixed(v % 1 === 0 ? 0 : 1);
};

/** 多序列折线图（带面积填充 + 十字线 tooltip）。 */
export function MetricChart({ series, height = 200, unit = '' }: { series: SeriesDef[]; height?: number; unit?: string }) {
  const svgRef = useRef<SVGSVGElement>(null);
  const [hoverX, setHoverX] = useState<number | null>(null); // svg 坐标 px

  const padL = 44;
  const padR = 12;
  const padT = 10;
  const padB = 24;
  const innerW = 760 - padL - padR; // viewBox 宽 760，响应式缩放
  const innerH = height - padT - padB;

  const { paths, areas, dots, yTicks, xTicks, allPts } = useMemo(() => {
    const allV: number[] = [];
    for (const s of series) for (const p of s.data) if (Number.isFinite(p.value)) allV.push(p.value);
    let mn = allV.length ? Math.min(...allV) : 0;
    let mx = allV.length ? Math.max(...allV) : 1;
    if (mn === mx) {
      mn -= 1;
      mx += 1;
    } else {
      const pad = (mx - mn) * 0.08;
      mn = Math.max(0, mn - pad);
      mx = mx + pad;
    }
    const span = mx - mn || 1;

    const times = series[0]?.data.map((p) => p.time * 1000) ?? [];
    const tMin = times.length ? Math.min(...times) : 0;
    const tMax = times.length ? Math.max(...times) : 1;
    const tSpan = tMax - tMin || 1;

    const x = (t: number) => padL + ((t - tMin) / tSpan) * innerW;
    const y = (v: number) => padT + innerH - ((v - mn) / span) * innerH;

    const paths = series.map((s) => {
      if (!s.data.length) return '';
      return s.data.map((p, i) => `${i === 0 ? 'M' : 'L'} ${x(p.time * 1000).toFixed(1)} ${y(p.value).toFixed(1)}`).join(' ');
    });
    const areas = series.map((s) => {
      if (!s.data.length) return '';
      const line = s.data.map((p) => `L ${x(p.time * 1000).toFixed(1)} ${y(p.value).toFixed(1)}`).join(' ');
      const last = x(s.data[s.data.length - 1].time * 1000);
      const first = x(s.data[0].time * 1000);
      return `M ${first.toFixed(1)} ${(padT + innerH).toFixed(1)} ${line} L ${last.toFixed(1)} ${(padT + innerH).toFixed(1)} Z`;
    });
    const dots = series.map((s) => {
      const last = s.data.at(-1);
      return last ? { x: x(last.time * 1000), y: y(last.value), color: s.color } : null;
    });

    // y 轴 4 刻度
    const yTicks = Array.from({ length: 5 }, (_, i) => {
      const v = mn + (span * i) / 4;
      return { v, y: y(v) };
    });
    // x 轴 5 刻度
    const xTicks = Array.from({ length: 5 }, (_, i) => {
      const t = tMin + (tSpan * i) / 4;
      return { t, x: x(t) };
    });

    const allPts = series.flatMap((s) => s.data.map((p) => ({ time: p.time * 1000, x: x(p.time * 1000), series: s.label, color: s.color, value: p.value })));

    return { paths, areas, dots, yTicks, xTicks, allPts };
  }, [series, innerW, innerH, padL, padT, padB]);

  // 十字线：吸附 hoverX 到最近数据点 X
  const nearest = useMemo(() => {
    if (hoverX === null || !allPts.length) return null;
    let best = allPts[0];
    let bestD = Infinity;
    for (const p of allPts) {
      const d = Math.abs(p.x - hoverX);
      if (d < bestD) {
        bestD = d;
        best = p;
      }
    }
    const snapX = best.x;
    const atX = allPts.filter((p) => Math.abs(p.x - snapX) < 0.5);
    return { snapX, atX: atX.length ? atX : [best] };
  }, [hoverX, allPts]);

  const onMove = (e: React.PointerEvent<SVGSVGElement>) => {
    const svg = svgRef.current;
    if (!svg) return;
    const pt = svg.createSVGPoint();
    pt.x = e.clientX;
    pt.y = e.clientY;
    const ctm = svg.getScreenCTM();
    if (!ctm) return;
    const local = pt.matrixTransform(ctm.inverse());
    setHoverX(local.x);
  };

  const single = series.length === 1;

  return (
    <div className="relative w-full">
      {/* 多序列图例 */}
      {!single && (
        <div className="mb-2 flex flex-wrap items-center gap-x-4 gap-y-1">
          {series.map((s) => (
            <span key={s.label} className="inline-flex items-center gap-1.5 text-xs" style={{ color: 'var(--mut)' }}>
              <span className="inline-block h-0.5 w-4 rounded" style={{ background: s.color }} />
              {s.label}
            </span>
          ))}
        </div>
      )}

      <svg
        ref={svgRef}
        viewBox={`0 0 760 ${height}`}
        className="w-full"
        style={{ display: 'block' }}
        onPointerMove={onMove}
        onPointerLeave={() => setHoverX(null)}
      >
        {/* 网格线 hairline */}
        {yTicks.map((t, i) => (
          <line key={`g${i}`} x1={padL} x2={760 - padR} y1={t.y} y2={t.y} stroke="var(--grid)" strokeWidth={1} />
        ))}
        {/* y 刻度文字 */}
        {yTicks.map((t, i) => (
          <text key={`yl${i}`} x={padL - 8} y={t.y + 3} textAnchor="end" fontSize={11} fill="var(--faint)" className="num">
            {fmtVal(t.v)}
          </text>
        ))}
        {/* x 刻度 */}
        {xTicks.map((t, i) => (
          <text key={`xl${i}`} x={t.x} y={height - 6} textAnchor={i === 0 ? 'start' : i === xTicks.length - 1 ? 'end' : 'middle'} fontSize={11} fill="var(--faint)" className="num">
            {fmtMs(t.t).slice(6)}
          </text>
        ))}

        {/* 面积填充 */}
        {areas.map((a, i) => (
          <path key={`a${i}`} d={a} fill={series[i].color} opacity={0.1} />
        ))}
        {/* 折线 */}
        {paths.map((p, i) => (
          <path key={`p${i}`} d={p} fill="none" stroke={series[i].color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
        ))}
        {/* 端点圆点 + 表面色环 */}
        {dots.map((d, i) =>
          d ? (
            <g key={`d${i}`}>
              <circle cx={d.x} cy={d.y} r={5} fill="var(--panel)" />
              <circle cx={d.x} cy={d.y} r={3.5} fill={d.color} />
            </g>
          ) : null,
        )}

        {/* 十字线 */}
        {nearest && (
          <line x1={nearest.snapX} x2={nearest.snapX} y1={padT} y2={padT + innerH} stroke="var(--axis)" strokeWidth={1} strokeDasharray="3 3" />
        )}
      </svg>

      {/* tooltip */}
      {nearest && (
        <div
          className="pointer-events-none absolute z-10 rounded-lg px-3 py-2 text-xs"
          style={{
            background: 'var(--panel)',
            boxShadow: 'var(--shadow-pop)',
            border: '1px solid var(--line)',
            left: `${(Math.min(Math.max(nearest.snapX, padL + 60), 760 - padR - 60) / 760) * 100}%`,
            top: 4,
            transform: 'translateX(-50%)',
            whiteSpace: 'nowrap',
          }}
        >
          <div className="mb-1 font-medium" style={{ color: 'var(--mut)' }}>
            {fmtMs(nearest.atX[0].time)}
          </div>
          {nearest.atX.map((p, i) => (
            <div key={i} className="flex items-center justify-between gap-3">
              <span className="inline-flex items-center gap-1.5" style={{ color: 'var(--mut)' }}>
                <span className="inline-block h-0.5 w-3 rounded" style={{ background: p.color }} />
                {p.series}
              </span>
              <span className="num font-medium" style={{ color: 'var(--fg)' }}>
                {p.value.toFixed(1)}
                {unit}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

/** 极简迷你折线（sparkline）：无坐标轴，主机卡片趋势小图。 */
export function Sparkline({ data, color, height = 34 }: { data: Point[]; color: string; height?: number }) {
  const { line, area, dot } = useMemo(() => {
    if (!data.length) return { line: '', area: '', dot: null };
    const w = 120;
    const h = height;
    const vs = data.map((p) => p.value);
    const mn = Math.min(...vs);
    const mx = Math.max(...vs);
    const span = mx - mn || 1;
    const x = (i: number) => (i / (data.length - 1)) * w;
    const y = (v: number) => h - 3 - ((v - mn) / span) * (h - 6);
    const line = data.map((p, i) => `${i === 0 ? 'M' : 'L'} ${x(i).toFixed(1)} ${y(p.value).toFixed(1)}`).join(' ');
    const area = `${line} L ${w} ${h} L 0 ${h} Z`;
    const last = data.at(-1)!;
    return { line, area, dot: { x: x(data.length - 1), y: y(last.value) } };
  }, [data, height]);

  if (!data.length) return <div style={{ height }} />;

  return (
    <svg viewBox={`0 0 120 ${height}`} className="w-full" style={{ display: 'block', height }}>
      <path d={area} fill={color} opacity={0.12} />
      <path d={line} fill="none" stroke={color} strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" />
      {dot && (
        <>
          <circle cx={dot.x} cy={dot.y} r={3} fill="var(--panel)" />
          <circle cx={dot.x} cy={dot.y} r={2} fill={color} />
        </>
      )}
    </svg>
  );
}

/** 迷你进度条（主机卡片 CPU/内存/磁盘）：阈值配色 + 撑满。 */
export function MiniMeter({ label, value, color }: { label: ReactNode; value: number; color?: string }) {
  const c = color ?? metricColor(value);
  return (
    <div>
      <div className="mb-1 flex items-center justify-between text-xs">
        <span style={{ color: 'var(--mut)' }}>{label}</span>
        <span className="num font-medium" style={{ color: 'var(--fg)' }}>
          {value.toFixed(1)}%
        </span>
      </div>
      <div className="mini-track">
        <div className="mini-fill" style={{ width: `${Math.min(100, value)}%`, background: c }} />
      </div>
    </div>
  );
}
