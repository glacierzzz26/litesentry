// 与 server/internal/alert/engine.go 的指标集保持一致。

export const METRIC_LABELS: Record<string, string> = {
  cpu_pct: 'CPU 使用率',
  mem_pct: '内存使用率',
  load_1m: '负载 1m',
  disk_pct: '磁盘使用率',
  container_cpu: '容器 CPU',
  container_mem: '容器内存',
  container_down: '容器停止',
  offline: '节点离线',
};
