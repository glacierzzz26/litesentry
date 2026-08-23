// 登录页 —— 沿用 Beszel/Uptime Kuma 浅色风格：浅底 + 居中卡片 + LS 标识。
// 用户名 + 密码换 JWT；错误展示统一文案；mock 模式任意非空凭据即登录。

import { useState } from 'react';
import { Button, Input } from 'antd';
import { LockOutlined, UserOutlined } from '@ant-design/icons';
import { useStore } from '../store';

export default function Login() {
  const login = useStore((s) => s.login);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState('');

  const submit = async (e?: React.FormEvent) => {
    e?.preventDefault();
    const u = username.trim();
    if (!u || !password) {
      setErr('请输入用户名和密码');
      return;
    }
    setErr('');
    setLoading(true);
    const errMsg = await login(u, password);
    setLoading(false);
    if (errMsg) setErr(errMsg);
  };

  return (
    <div className="flex min-h-screen items-center justify-center px-4" style={{ background: 'var(--surface)' }}>
      <div className="w-full max-w-sm">
        {/* 标识 */}
        <div className="mb-8 flex flex-col items-center">
          <span
            className="mb-3 flex h-12 w-12 items-center justify-center rounded-xl text-lg font-bold"
            style={{ color: 'var(--accent)', background: 'color-mix(in srgb, var(--accent) 12%, transparent)' }}
          >
            LS
          </span>
          <h1 className="text-xl font-semibold" style={{ color: 'var(--fg)' }}>
            litesentry
          </h1>
          <p className="mt-1 text-xs" style={{ color: 'var(--mut)' }}>
            轻量主机 + 容器监控
          </p>
        </div>

        {/* 登录卡片 */}
        <form onSubmit={submit} className="card p-6">
          <label className="eyebrow mb-2 block">用户名</label>
          <Input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder="用户名"
            prefix={<UserOutlined style={{ color: 'var(--faint)' }} />}
            size="large"
            autoFocus
            status={err ? 'error' : ''}
          />
          <label className="eyebrow mb-2 mt-4 block">密码</label>
          <Input.Password
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="密码"
            prefix={<LockOutlined style={{ color: 'var(--faint)' }} />}
            size="large"
            status={err ? 'error' : ''}
            onPressEnter={() => submit()}
          />
          {err && (
            <div className="mt-2 text-xs" style={{ color: 'var(--down)' }}>
              {err}
            </div>
          )}
          <Button type="primary" htmlType="submit" loading={loading} block size="large" className="mt-4">
            登录
          </Button>
          <p className="mt-4 text-center text-xs" style={{ color: 'var(--faint)' }}>
            首次部署默认 <code className="num">admin</code>（由 -bootstrap-user/-bootstrap-pass 指定），首次登录需修改密码
          </p>
        </form>
      </div>
    </div>
  );
}
