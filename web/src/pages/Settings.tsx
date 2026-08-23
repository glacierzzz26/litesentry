// 设置：用户管理（面板登录账号，bcrypt 加密）+ 飞书机器人（webhook + 签名 secret）。
// 用户表格沿用主机/容器页的 antd Table 样式（.card 包裹 + scroll + 首末列留白）。

import { useCallback, useEffect, useState } from 'react';
import { App, Button, Checkbox, Form, Input, Modal, Popconfirm, Space, Table, Tag, Tooltip } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { api } from '../api';
import { fmtDateTime } from '../format';
import { useStore } from '../store';
import type { SettingsView, User } from '../types';

export default function Settings() {
  const { message } = App.useApp();
  const me = useStore((s) => s.user);
  const [settings, setSettings] = useState<SettingsView | null>(null);
  const [sending, setSending] = useState(false);
  const [feishuForm] = Form.useForm();

  // ---- 用户管理状态 ----
  const [users, setUsers] = useState<User[]>([]);
  const [usersLoading, setUsersLoading] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [createForm] = Form.useForm();
  const [resetTarget, setResetTarget] = useState<User | null>(null);
  const [resetForm] = Form.useForm();
  const [saving, setSaving] = useState(false);

  const loadSettings = useCallback(async () => {
    try {
      const s = await api.settings();
      setSettings(s);
      feishuForm.setFieldsValue({ webhook: s.feishu_webhook, secret: '', clearSecret: false });
    } catch {
      /* mock 模式静默 */
    }
  }, [feishuForm]);

  const loadUsers = useCallback(async () => {
    setUsersLoading(true);
    try {
      setUsers(await api.users());
    } catch {
      /* mock 模式静默 */
    } finally {
      setUsersLoading(false);
    }
  }, []);

  useEffect(() => {
    loadSettings();
    loadUsers();
  }, [loadSettings, loadUsers]);

  // ---- 用户管理 ----
  const create = async () => {
    const v = await createForm.validateFields();
    setSaving(true);
    try {
      const u = await api.createUser({ username: v.username, password: v.password, display_name: v.display_name });
      message.success(`已创建用户 ${u.username}，首次登录需修改密码`);
      setCreateOpen(false);
      createForm.resetFields();
      loadUsers();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    } finally {
      setSaving(false);
    }
  };

  const resetPwd = async () => {
    if (!resetTarget) return;
    const v = await resetForm.validateFields();
    setSaving(true);
    try {
      await api.resetPassword(resetTarget.id, v.password);
      message.success(`已重置 ${resetTarget.username} 的密码，下次登录需修改密码`);
      setResetTarget(null);
      resetForm.resetFields();
      loadUsers();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (u: User) => {
    try {
      await api.deleteUser(u.id);
      message.success(`已删除用户 ${u.username}`);
      loadUsers();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  const columns = [
    {
      title: '用户名',
      key: 'username',
      width: 200,
      ellipsis: true,
      render: (_: unknown, u: User) => (
        <div className="min-w-0">
          <span className="block truncate font-medium" style={{ color: 'var(--fg)' }}>{u.username}</span>
          {u.display_name && (
            <span className="block truncate text-xs" style={{ color: 'var(--mut)' }}>{u.display_name}</span>
          )}
        </div>
      ),
    },
    {
      title: '角色',
      key: 'role',
      width: 90,
      render: (_: unknown, u: User) => (
        <span className="num text-xs" style={{ color: 'var(--mut)' }}>{u.role}</span>
      ),
    },
    {
      title: '上次登录',
      key: 'last_login',
      width: 170,
      render: (_: unknown, u: User) =>
        u.last_login_at ? (
          <span className="num text-xs" style={{ color: 'var(--mut)' }}>{fmtDateTime(u.last_login_at)}</span>
        ) : (
          <span className="text-xs" style={{ color: 'var(--faint)' }}>从未登录</span>
        ),
    },
    {
      title: '创建时间',
      key: 'created',
      width: 170,
      render: (_: unknown, u: User) => (
        <span className="num text-xs" style={{ color: 'var(--mut)' }}>{fmtDateTime(u.created_at)}</span>
      ),
    },
    {
      title: '状态',
      key: 'must_change',
      width: 90,
      render: (_: unknown, u: User) =>
        u.must_change_password ? <Tag color="warning">待改密</Tag> : <Tag color="success">正常</Tag>,
    },
    {
      title: '操作',
      key: 'action',
      width: 150,
      render: (_: unknown, u: User) => {
        const isSelf = me?.id === u.id;
        const isLast = users.length <= 1;
        const delDisabled = isSelf || isLast;
        return (
          <div className="flex items-center gap-1">
            <Button size="small" type="link" onClick={() => { setResetTarget(u); resetForm.resetFields(); }}>
              重置密码
            </Button>
            <Tooltip title={isSelf ? '不能删除当前登录用户' : isLast ? '不能删除最后一个用户' : ''}>
              <Popconfirm title={`确认删除用户 ${u.username}？`} onConfirm={() => remove(u)} disabled={delDisabled}>
                <Button size="small" type="link" danger disabled={delDisabled}>
                  删除
                </Button>
              </Popconfirm>
            </Tooltip>
          </div>
        );
      },
    },
  ];

  const saveFeishu = async () => {
    const v = await feishuForm.validateFields();
    try {
      const res = await api.saveSettings({
        feishu_webhook: v.webhook ?? '',
        feishu_secret: v.secret ?? '',
        feishu_secret_clear: v.clearSecret === true,
      });
      setSettings(res);
      feishuForm.setFieldsValue({ secret: '', clearSecret: false });
      message.success('飞书机器人配置已保存');
    } catch (e) {
      message.error(String(e));
    }
  };

  const test = async () => {
    setSending(true);
    try {
      const res = await api.feishuTest();
      message.success(res.message || '测试消息已发送');
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="grid max-w-5xl gap-4">
      {/* 用户管理 */}
      <section className="card p-5">
        <div className="mb-1 flex items-center justify-between">
          <h3 className="text-sm font-semibold" style={{ color: 'var(--fg)' }}>用户管理</h3>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => { createForm.resetFields(); setCreateOpen(true); }}>
            新建用户
          </Button>
        </div>
        <p className="mb-4 text-xs" style={{ color: 'var(--mut)' }}>
          面板登录账号，密码 bcrypt 加密存储。新建用户 / 重置密码后首次登录需修改密码。
        </p>
        <Table<User>
          rowKey="id"
          size="middle"
          loading={usersLoading && users.length === 0}
          dataSource={users}
          pagination={false}
          scroll={{ x: 880 }}
          locale={{ emptyText: '暂无用户' }}
          columns={columns}
        />
      </section>

      {/* 飞书机器人 */}
      <section className="card p-5">
        <h3 className="mb-1 text-sm font-semibold" style={{ color: 'var(--fg)' }}>飞书机器人</h3>
        <p className="mb-4 text-xs" style={{ color: 'var(--mut)' }}>
          阈值告警推送到飞书自定义机器人，HMAC-SHA256 签名。
        </p>
        <Form form={feishuForm} layout="vertical" style={{ maxWidth: 560 }}>
          <Form.Item name="webhook" label="Webhook 地址" extra="自定义机器人 → 复制 webhook（https://open.feishu.cn/open-apis/bot/v2/hook/...）">
            <Input placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/xxx" />
          </Form.Item>
          <Form.Item
            name="secret"
            label="签名密钥（secret）"
            extra={settings?.feishu_secret_set ? '已保存。留空 = 不修改；如需清除勾选下方复选框。' : '未设置。留空 = 不设置。'}
          >
            <Input.Password placeholder={settings?.feishu_secret_set ? '已保存（留空 = 不修改）' : '签名 secret（可选）'} />
          </Form.Item>
          <Form.Item name="clearSecret" valuePropName="checked">
            <Checkbox>清除已保存的 secret</Checkbox>
          </Form.Item>
          <Space>
            <Button type="primary" onClick={saveFeishu}>保存配置</Button>
            <Button onClick={test} loading={sending} disabled={!settings?.feishu_webhook}>发送测试消息</Button>
          </Space>
        </Form>
        <p className="mt-4 text-xs" style={{ color: 'var(--faint)' }}>
          ℹ️ 测试阶段 webhook 与 secret 明文存于服务端数据库（不写入日志）；上线前需加密存储并启用 TLS/mTLS。
        </p>
      </section>

      {/* 新建用户 */}
      <Modal
        title="新建用户"
        open={createOpen}
        onOk={create}
        onCancel={() => setCreateOpen(false)}
        okText="创建"
        cancelText="取消"
        width={420}
        confirmLoading={saving}
      >
        <Form<User> form={createForm} layout="vertical" style={{ marginTop: 12 }}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input placeholder="登录用户名" autoFocus />
          </Form.Item>
          <Form.Item
            name="password"
            label="初始密码"
            rules={[
              { required: true, message: '请输入初始密码' },
              { min: 8, message: '至少 8 位' },
            ]}
          >
            <Input.Password placeholder="至少 8 位" />
          </Form.Item>
          <Form.Item name="display_name" label="显示名（可选）">
            <Input placeholder="如：运维" />
          </Form.Item>
        </Form>
        <p className="text-xs" style={{ color: 'var(--faint)' }}>
          新用户首次登录将强制修改密码。
        </p>
      </Modal>

      {/* 重置密码 */}
      <Modal
        title={resetTarget ? `重置 ${resetTarget.username} 的密码` : '重置密码'}
        open={!!resetTarget}
        onOk={resetPwd}
        onCancel={() => setResetTarget(null)}
        okText="重置"
        cancelText="取消"
        width={420}
        confirmLoading={saving}
      >
        <Form form={resetForm} layout="vertical" style={{ marginTop: 12 }}>
          <Form.Item
            name="password"
            label="新密码"
            rules={[
              { required: true, message: '请输入新密码' },
              { min: 8, message: '至少 8 位' },
            ]}
          >
            <Input.Password placeholder="至少 8 位" autoFocus />
          </Form.Item>
        </Form>
        <p className="text-xs" style={{ color: 'var(--faint)' }}>
          重置后该用户下次登录需修改密码。
        </p>
      </Modal>
    </div>
  );
}
