# litesentry · Web 前端

唯一入口：`index.html` → `src/`（v2 前端，按 `litesentry-prototype-v2.html` 重建），**直连后端真实数据，无 mock**。

- 技术栈：Vite + React 19 + antd v5 + Tailwind CSS v4 + TypeScript（`noUnusedLocals` / `verbatimModuleSyntax` / `erasableSyntaxOnly`）。
- 数据层：`src/api.ts`（REST）· `src/store.ts`（zustand 状态）· `src/types.ts` · `src/format.ts` · `src/constants.ts` · `src/route.ts`（hash 路由）。
- 可视化：`src/components/Chart.tsx`（手写 SVG：MetricChart / Sparkline / MiniMeter）、`src/components/ui.tsx`（标签/状态组件）。
- 页面：`src/pages/`（总览 / 主机 / 节点详情 / 容器 / 容器详情 / 告警事件 / 告警规则 / 设置）。
- 主题：深/浅色切换，`<html data-theme>` + antd darkAlgorithm；防闪脚本在 `index.html` 内联按 `localStorage.ls_theme` 预置；深色令牌定义在 `src/index.css` 的 `[data-theme="dark"]`，v2 布局样式在 `src/app.css`。
- 指标配色统一走 `src/format.ts` 的 `metricColor()`：≤60 绿 · 60–85 琥珀 · >85 红。

## 开发

```bash
npm install
npm run dev        # http://localhost:5173，/api 与 /docs 代理到后端
```

后端地址默认 `http://127.0.0.1:8080`，可用环境变量覆盖：

```bash
LS_API_PROXY=http://localhost:8088 npm run dev
```

## 构建

```bash
npm run build      # 输出 dist/（单入口）
```

根目录 `make web` 会把 `dist/*` 复制进 `server/internal/webui/dist`，随 Go server 一起 `go:embed` 编译进单个二进制。
