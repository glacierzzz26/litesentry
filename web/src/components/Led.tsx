// 状态 LED 圆点：on（在线，绿）/ warn（高负载，琥珀）/ off（离线，灰）。
// 与原型 .led.on/.warn/.off 一致；颜色走令牌随主题。

export type LedState = 'on' | 'warn' | 'off';

export function Led({ state }: { state: LedState }) {
  return <span className={`v2-led ${state}`} />;
}
