// 设置：用户管理（面板登录账号，bcrypt 加密）+ 告警通知（nexus 事件中心）。
// 用户表格沿用主机/容器页的 antd Table 样式（.card 包裹 + scroll + 首末列留白）。

import { useCallback, useEffect, useState } from 'react';
import { App, Button, Checkbox, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, Tooltip } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { api } from '../api';
import { fmtDateTime } from '../format';
import { useStore } from '../store';
import type { Agent, SettingsView, User } from '../types';

export default function Settings() {
  const { message } = App.useApp();
  const me = useStore((s) => s.user);
  const [settings, setSettings] = useState<SettingsView | null>(null);
  const [sending, setSending] = useState(false);
  const [nexusForm] = Form.useForm();
  const [serverForm] = Form.useForm();
  // 公网机候选：节点列表。展示主机名（agent_id 不露给用户），值仍是 agent_id。
  const [agents, setAgents] = useState<Agent[]>([]);

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
      nexusForm.setFieldsValue({ url: s.nexus_url, source: s.nexus_source, token: '', clearToken: false });
      serverForm.setFieldsValue({ agent_id: s.server_agent_id || undefined });
    } catch {
      /* mock 模式静默 */
    }
  }, [nexusForm, serverForm]);

  const loadAgents = useCallback(async () => {
    try {
      setAgents(await api.agents());
    } catch {
      /* mock 模式静默 */
    }
  }, []);

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
    loadAgents();
  }, [loadSettings, loadUsers, loadAgents]);

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

  const saveNexus = async () => {
    const v = await nexusForm.validateFields();
    try {
      const res = await api.saveSettings({
        nexus_url: v.url ?? '',
        nexus_source: v.source ?? '',
        nexus_ingest_token: v.token ?? '',
        nexus_ingest_token_clear: v.clearToken === true,
      });
      setSettings(res);
      nexusForm.setFieldsValue({ token: '', clearToken: false });
      message.success('告警通知配置已保存');
    } catch (e) {
      message.error(String(e));
    }
  };

  const saveServerAgent = async () => {
    const v = await serverForm.validateFields();
    try {
      // 只提交公网机字段：nexus 表单各写各的（后端 nexus_* 为指针语义，缺省即不修改）。
      const res = await api.saveSettings({
        server_agent_id: (v.agent_id as string) ?? '',
      });
      setSettings(res);
      message.success(v.agent_id ? '公网机已设置' : '公网机声明已清除');
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  const test = async () => {
    setSending(true);
    try {
      const res = await api.notifyTest();
      message.success(res.message || '测试事件已上报 nexus');
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

      {/* 告警通知（nexus） */}
      <section className="card p-5">
        <h3 className="mb-1 text-sm font-semibold" style={{ color: 'var(--fg)' }}>告警通知（Nexus）</h3>
        <p className="mb-4 text-xs" style={{ color: 'var(--mut)' }}>
          阈值告警（触发 + 恢复）上报到 nexus 事件中心，由 nexus 统一去重、路由并发送飞书通知。本服务不再直连飞书。
        </p>
        <Form form={nexusForm} layout="vertical" style={{ maxWidth: 560 }}>
          <Form.Item name="url" label="Nexus 地址" extra="事件上报入口，如 https://nexus.5home.online">
            <Input placeholder="https://nexus.5home.online" />
          </Form.Item>
          <Form.Item name="source" label="数据源（source）" extra="留空默认 litesentry。nexus 按此标记来源，便于筛选与路由。">
            <Input placeholder="litesentry" />
          </Form.Item>
          <Form.Item
            name="token"
            label="Ingest Token"
            extra={settings?.nexus_ingest_token_set ? '已保存。留空 = 不修改；如需清除勾选下方复选框。' : '未设置。留空 = 不设置。'}
          >
            <Input.Password placeholder={settings?.nexus_ingest_token_set ? '已保存（留空 = 不修改）' : 'Bearer ingest token'} />
          </Form.Item>
          <Form.Item name="clearToken" valuePropName="checked">
            <Checkbox>清除已保存的 token</Checkbox>
          </Form.Item>
          <Space>
            <Button type="primary" onClick={saveNexus}>保存配置</Button>
            <Button onClick={test} loading={sending} disabled={!settings?.nexus_url || !settings?.nexus_ingest_token_set}>
              发送测试事件
            </Button>
          </Space>
        </Form>
        <p className="mt-4 text-xs" style={{ color: 'var(--faint)' }}>
          ℹ️ Token 明文存于服务端数据库（不写入日志、不回传前端）；无 TLS 时请自行评估风险。
        </p>
      </section>

      {/* 公网机（Server 同机 Agent）：frps 归属 / 任务目标 / 内置插件指派都依赖它 */}
      <section className="card p-5">
        <h3 className="mb-1 text-sm font-semibold" style={{ color: 'var(--fg)' }}>公网机（Server 同机 Agent）</h3>
        <p className="mb-4 text-xs" style={{ color: 'var(--mut)' }}>
          声明哪台节点就是 Server 本机 —— frps 归属、任务「公网机」目标、内置插件指派都按它解析。
          未声明时这些功能会提示「未设置公网机 Agent」。
        </p>
        <Form form={serverForm} layout="vertical" style={{ maxWidth: 560 }}>
          <Form.Item
            name="agent_id"
            label="节点"
            extra={settings?.server_agent_id ? '已设置。留空并保存 = 清除声明。' : '未设置 —— 请在下面选择 Server 所在节点。'}
          >
            <Select
              allowClear
              placeholder="选择 Server 所在节点（按主机名）"
              options={agents.map((a) => ({ value: a.agent_id, label: a.hostname || '未知节点' }))}
            />
          </Form.Item>
          <Button type="primary" onClick={saveServerAgent}>保存</Button>
        </Form>
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
