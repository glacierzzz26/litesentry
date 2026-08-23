// v2 统计卡：图标 + 标签 + 大数值，整卡可点击跳转。

import type { ReactNode } from 'react';
import { navigate } from '../../src/route';

export function StatCard({ label, value, suffix, icon, bg, color, to }: {
  label: string;
  value: ReactNode;
  suffix?: string;
  icon: ReactNode;
  bg: string;
  color: string;
  to: string;
}) {
  return (
    <button className="v2-stat" onClick={() => navigate(to)}>
      <div className="top">
        <span className="ico" style={{ background: bg, color }}>
          {icon}
        </span>
      </div>
      <div className="l">{label}</div>
      <div className="v num">
        {value}
        {suffix && <small> {suffix}</small>}
      </div>
    </button>
  );
}
