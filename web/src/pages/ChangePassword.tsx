// 强制修改密码页 —— 首登/被重置密码后进入面板前展示。
// 视觉严格沿用登录页：浅底居中卡片 + LS 标识 + 输入框。

import { useState } from 'react';
import { App, Button, Input } from 'antd';
import { LockOutlined } from '@ant-design/icons';
import { useStore } from '../store';

export default function ChangePassword() {
  const changePassword = useStore((s) => s.changePassword);
  const logout = useStore((s) => s.logout);
  const user = useStore((s) => s.user);
  const { message } = App.useApp();

  const [oldPwd, setOldPwd] = useState('');
  const [newPwd, setNewPwd] = useState('');
  const [confirm, setConfirm] = useState('');
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState('');

  const submit = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!oldPwd || !newPwd) {
      setErr('请输入旧密码和新密码');
      return;
    }
    if (newPwd.length < 8) {
      setErr('新密码至少 8 位');
      return;
    }
    if (newPwd !== confirm) {
      setErr('两次输入的新密码不一致');
      return;
    }
    setErr('');
    setLoading(true);
    const e2 = await changePassword(oldPwd, newPwd);
    setLoading(false);
    if (e2) {
      setErr(e2);
    } else {
      message.success('密码已修改');
    }
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
            修改密码
          </h1>
          <p className="mt-1 text-xs" style={{ color: 'var(--mut)' }}>
            首次登录 / 管理员重置后需设置新密码{user?.username ? `（${user.username}）` : ''}
          </p>
        </div>

        {/* 改密卡片 */}
        <form onSubmit={submit} className="card p-6">
          <label className="eyebrow mb-2 block">当前密码</label>
          <Input.Password
            value={oldPwd}
            onChange={(e) => setOldPwd(e.target.value)}
            placeholder="当前密码"
            prefix={<LockOutlined style={{ color: 'var(--faint)' }} />}
            size="large"
            autoFocus
            status={err ? 'error' : ''}
          />
          <label className="eyebrow mb-2 mt-4 block">新密码</label>
          <Input.Password
            value={newPwd}
            onChange={(e) => setNewPwd(e.target.value)}
            placeholder="至少 8 位"
            prefix={<LockOutlined style={{ color: 'var(--faint)' }} />}
            size="large"
            status={err ? 'error' : ''}
          />
          <label className="eyebrow mb-2 mt-4 block">确认新密码</label>
          <Input.Password
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            placeholder="再次输入新密码"
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
            确认修改
          </Button>
          <Button type="link" block size="small" className="mt-1" style={{ color: 'var(--faint)' }} onClick={logout}>
            退出登录
          </Button>
        </form>
      </div>
    </div>
  );
}
