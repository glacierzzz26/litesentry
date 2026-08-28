// FRP 隧道管理（阶段二 S3）：
//   配置列表（目标主机名 + 类型 + 启用开关 + 运行状态 + 隧道状态表）+ 新建/编辑 Modal + 删除。
// 安全：token 仅写入不回显（编辑留空 = 保留原值）；agent_id 永不展示，一律显示主机名。
// 流量：frp 0.71 admin API 只返回隧道状态（running/err），无 per-tunnel 流量字段 → 不展示 RX/TX 列
//（proto 的 rx/tx 字段保留，frp 后续版本暴露流量后再填充）。

import { useCallback, useEffect, useState } from 'react';
import { App, Button, Form, Input, InputNumber, Modal, Popconfirm, Segmented, Select, Skeleton, Switch, Tag } from 'antd';
import { MinusCircleOutlined, PlusOutlined } from '@ant-design/icons';
import { api } from '../api';
import type { Agent, FrpConfigView, FrpStatus, FrpTunnel, SaveFrpBody } from '../types';

const KIND_LABEL: Record<string, string> = { frps: '公网机', frpc: '内网节点' };
const TYPE_OPTIONS = ['tcp', 'udp', 'http', 'https', 'stcp', 'xtcp'].map((v) => ({ value: v, label: v }));

/** 运行状态徽标：未上报 / 未运行（带原因）/ 运行中（在线隧道数）。 */
function StatusBadge({ status }: { status?: FrpStatus }) {
  if (!status) return <Tag>未上报</Tag>;
  if (!status.running) return <Tag color="red" title={status.error}>未运行</Tag>;
  const online = status.tunnels.filter((t) => t.status === 'online' || t.status === 'running').length;
  return (
    <Tag color="green">
      运行中{status.tunnels.length > 0 ? ` · ${online}/${status.tunnels.length} 在线` : ''}
    </Tag>
  );
}

/** 每隧道状态列（frp 各版本取值不一：老版 online，0.71+ running）。 */
function tunnelStatusTag(s: string) {
  if (s === 'online' || s === 'running') return <Tag color="green">{s}</Tag>;
  if (s === 'offline' || s === 'closed') return <Tag>{s}</Tag>;
  return <Tag color="red">{s || 'unknown'}</Tag>;
}

export default function Frp() {
  const { message } = App.useApp();
  const [rows, setRows] = useState<FrpConfigView[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<FrpConfigView | null>(null);
  const [newKind, setNewKind] = useState<'frps' | 'frpc'>('frps');
  const [form] = Form.useForm<Record<string, unknown>>();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setRows(await api.frpConfigs());
    } catch {
      /* 网络/服务异常静默，保留旧数据 */
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, 15_000);
    return () => clearInterval(t);
  }, [load]);

  const openNew = () => {
    setEditing(null);
    setNewKind('frps');
    form.resetFields();
    form.setFieldsValue({ enabled: true, server_port: 7000, proxies: [] });
    api.agents().then(setAgents).catch(() => setAgents([]));
    setModalOpen(true);
  };

  const openEdit = (r: FrpConfigView) => {
    setEditing(r);
    form.resetFields();
    form.setFieldsValue({ ...r, proxies: r.proxies ?? [] });
    setModalOpen(true);
  };

  const save = async () => {
    const values = form.getFieldsValue();
    const kind = editing ? editing.kind : newKind;
    const agentId = editing ? editing.agent_id : kind === 'frps' ? 'server' : (values.agent_id as string);
    if (!agentId) {
      message.error('请选择目标节点');
      return;
    }
    const body: SaveFrpBody = {
      server_addr: (values.server_addr as string) ?? '',
      server_port: values.server_port as number,
      token: (values.token as string) ?? '',
      proxies: (values.proxies as FrpTunnel[]) ?? [],
      enabled: (values.enabled as boolean) ?? true,
    };
    try {
      await api.saveFrpConfig(kind, agentId, body);
      message.success(editing ? '配置已更新，节点将按新配置重拉 frp' : '配置已保存，节点将拉起 frp');
      setModalOpen(false);
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  const remove = async (r: FrpConfigView) => {
    try {
      await api.deleteFrpConfig(r.kind, r.agent_id);
      message.success('配置已删除，节点将停掉 frp 进程');
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  const toggle = async (r: FrpConfigView, enabled: boolean) => {
    try {
      await api.saveFrpConfig(r.kind, r.agent_id, {
        server_addr: r.server_addr,
        server_port: r.server_port,
        proxies: r.proxies,
        enabled,
      });
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  if (loading && rows.length === 0) {
    return <div className="card p-4"><Skeleton active paragraph={{ rows: 6 }} /></div>;
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <span className="text-xs" style={{ color: 'var(--mut)' }}>
          frps = 公网机代理入口 · frpc = 内网节点隧道 · 配置经 Server 下发，节点每次心跳自动对齐
        </span>
        <Button type="primary" icon={<PlusOutlined />} onClick={openNew}>新建配置</Button>
      </div>

      {rows.length === 0 ? (
        <div className="card p-10 text-center text-sm" style={{ color: 'var(--mut)' }}>
          暂无 FRP 配置。点击「新建配置」为公网机建 frps、为内网节点建 frpc。
        </div>
      ) : (
        rows.map((r) => (
          <div key={`${r.kind}:${r.agent_id}`} className="card p-5 text-sm" style={{ borderColor: 'var(--line)' }}>
            <div className="flex flex-wrap items-center gap-3">
              <Tag color={r.kind === 'frps' ? 'blue' : 'purple'}>{KIND_LABEL[r.kind] ?? r.kind}</Tag>
              <span className="font-medium" style={{ color: 'var(--fg)' }}>{r.agent_name}</span>
              <span className="num text-xs" style={{ color: 'var(--mut)' }}>
                {r.kind === 'frpc' ? `${r.server_addr}:${r.server_port}` : `bind ${r.server_port}`}
              </span>
              <StatusBadge status={r.status} />
              {r.status?.error && (
                <span className="text-xs" style={{ color: 'var(--down)' }} title={r.status.error}>
                  {r.status.error}
                </span>
              )}
              <div className="ml-auto flex items-center gap-3">
                <Switch size="small" checked={r.enabled} onChange={(c) => toggle(r, c)} />
                <Button size="small" type="link" onClick={() => openEdit(r)}>编辑</Button>
                <Popconfirm title="删除后将停掉节点上的 frp 进程，确认？" onConfirm={() => remove(r)}>
                  <Button size="small" type="link" danger>删除</Button>
                </Popconfirm>
              </div>
            </div>

            {r.status && r.status.tunnels.length > 0 && (
              <table className="mt-3 w-full text-xs" style={{ borderCollapse: 'collapse' }}>
                <thead>
                  <tr style={{ color: 'var(--mut)' }}>
                    <th className="py-1.5 pr-3 text-left font-normal">隧道</th>
                    <th className="py-1.5 pr-3 text-left font-normal">类型</th>
                    <th className="py-1.5 pr-3 text-left font-normal">状态</th>
                    <th className="py-1.5 text-left font-normal">错误</th>
                  </tr>
                </thead>
                <tbody>
                  {r.status.tunnels.map((t) => (
                    <tr key={t.name} style={{ borderTop: '1px solid var(--line)' }}>
                      <td className="py-1.5 pr-3" style={{ color: 'var(--fg)' }}>{t.name}</td>
                      <td className="py-1.5 pr-3 num">{t.type}</td>
                      <td className="py-1.5 pr-3">
                        {tunnelStatusTag(t.status)}
                      </td>
                      <td className="py-1.5" style={{ color: t.err ? 'var(--down)' : 'var(--mut)' }}>{t.err || '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        ))
      )}

      <Modal
        title={editing ? '编辑 FRP 配置' : '新建 FRP 配置'}
        open={modalOpen}
        onOk={save}
        onCancel={() => setModalOpen(false)}
        okText="保存"
        cancelText="取消"
        width={620}
      >
        <Form<Record<string, unknown>> form={form} layout="vertical" style={{ marginTop: 12 }}>
          {editing ? (
            <div className="mb-4 flex items-center gap-2">
              <Tag color={editing.kind === 'frps' ? 'blue' : 'purple'}>{KIND_LABEL[editing.kind]}</Tag>
              <span style={{ color: 'var(--fg)' }}>{editing.agent_name}</span>
            </div>
          ) : (
            <>
              <Form.Item label="类型" required>
                <Segmented
                  options={[{ value: 'frps', label: 'frps（公网机入口）' }, { value: 'frpc', label: 'frpc（内网节点）' }]}
                  value={newKind}
                  onChange={(v) => setNewKind(v as 'frps' | 'frpc')}
                />
              </Form.Item>
              {newKind === 'frpc' && (
                <Form.Item name="agent_id" label="目标节点" rules={[{ required: true, message: '请选择目标节点' }]}>
                  <Select
                    showSearch
                    placeholder="选择内网节点（主机名）"
                    optionFilterProp="label"
                    options={agents.map((a) => ({ value: a.agent_id, label: a.hostname }))}
                  />
                </Form.Item>
              )}
              {newKind === 'frps' && (
                <div className="-mt-1 mb-3 text-xs" style={{ color: 'var(--mut)' }}>
                  公网机 = Server 同机 Agent（由设置 server_agent_id 声明）
                </div>
              )}
            </>
          )}

          <div className="grid grid-cols-2 gap-3">
            {newKind === 'frpc' && (
              <Form.Item name="server_addr" label="frps 地址" rules={[{ required: true, message: '请输入 frps 地址' }]}>
                <Input placeholder="例如 47.116.65.140" />
              </Form.Item>
            )}
            <Form.Item
              name="server_port"
              label={newKind === 'frpc' ? 'frps 端口' : '监听端口（bindPort）'}
              rules={[{ required: true, message: '请输入端口' }]}
            >
              <InputNumber style={{ width: '100%' }} min={1} max={65535} />
            </Form.Item>
            <Form.Item
              name="token"
              label="token（frp 认证 + admin 密码）"
              rules={editing ? [] : [{ required: true, message: '请输入 token' }]}
              extra={editing ? '留空 = 保留原值' : '配置下发到节点，仅管理员可见'}
            >
              <Input.Password placeholder={editing ? '留空保留原值' : '认证令牌'} />
            </Form.Item>
          </div>

          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>

          <Form.Item label="隧道（frpc 代理）">
            <Form.List name="proxies">
              {(fields, { add, remove }) => (
                <>
                  {fields.map(({ key, name, ...restField }) => (
                    <div key={key} className="mb-2 grid grid-cols-[1fr_84px_1fr_90px_90px_24px] items-center gap-2">
                      <Form.Item {...restField} name={[name, 'name']} rules={[{ required: true, message: '名称' }]} className="mb-0">
                        <Input placeholder="名称" size="small" />
                      </Form.Item>
                      <Form.Item {...restField} name={[name, 'type']} className="mb-0">
                        <Select size="small" options={TYPE_OPTIONS} placeholder="类型" />
                      </Form.Item>
                      <Form.Item {...restField} name={[name, 'local_ip']} className="mb-0">
                        <Input placeholder="local_ip" size="small" />
                      </Form.Item>
                      <Form.Item {...restField} name={[name, 'local_port']} className="mb-0">
                        <InputNumber placeholder="local_port" size="small" style={{ width: '100%' }} min={1} max={65535} />
                      </Form.Item>
                      <Form.Item {...restField} name={[name, 'remote_port']} className="mb-0">
                        <InputNumber placeholder="remote_port" size="small" style={{ width: '100%' }} min={1} max={65535} />
                      </Form.Item>
                      <Button type="text" size="small" icon={<MinusCircleOutlined />} onClick={() => remove(name)} />
                    </div>
                  ))}
                  <Button type="dashed" block icon={<PlusOutlined />} onClick={() => add({ type: 'tcp', local_ip: '127.0.0.1' })}>
                    添加隧道
                  </Button>
                </>
              )}
            </Form.List>
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
