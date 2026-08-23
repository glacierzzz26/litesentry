// 极简 hash 路由（不引入 react-router，保持轻量）。
// 路由：#/           → 总览
//        #/agent/:id → 节点详情
//        #/containers→ 容器
//        #/addresses → 地址表

import { useEffect, useState } from 'react';

export function parseHash(): string[] {
  const h = window.location.hash.replace(/^#\/?/, '');
  return h.split('/').filter(Boolean);
}

export function useHashRoute(): string[] {
  const [path, setPath] = useState<string[]>(parseHash());
  useEffect(() => {
    const on = () => setPath(parseHash());
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);
  return path;
}

export function navigate(to: string) {
  window.location.hash = to;
}
