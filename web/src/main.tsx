// 应用入口（唯一入口 index.html → 此模块）。
// 复用 index.css（设计令牌 + Tailwind）与 app.css（v2 布局）。
// 主题已在 index.html 内联脚本按 localStorage 预置到 <html data-theme>，此处直接沿用。

import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
// antd v5 + React 19 兼容补丁，必须在任何 antd 组件渲染前引入
import '@ant-design/v5-patch-for-react-19';
import './index.css';
import './app.css';
import App from './App';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
