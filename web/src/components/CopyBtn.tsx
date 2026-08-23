// 复制按钮：写入剪贴板并 toast（antd message）。用于节点地址表。

import { App } from 'antd';

export function CopyBtn({ text }: { text: string }) {
  const { message } = App.useApp();
  return (
    <button
      className="v2-copy"
      onClick={async (e) => {
        e.stopPropagation();
        try {
          await navigator.clipboard.writeText(text);
          message.success(`已复制: ${text}`);
        } catch {
          message.error('复制失败');
        }
      }}
    >
      复制
    </button>
  );
}
