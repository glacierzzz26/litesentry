// 插件仓库（阶段二）：Server 侧管理插件 —— 列表（按 id 分组各版本）/ 上传新版本 /
//   指派到节点 / 删除版本 / 取消指派。
// 语义：插件 = 不可变二进制版本（同 id 可并存多版本）；指派到节点后经 DesiredState
//   manifest 下发，节点下个心跳 FetchPlugin 拉取并执行。
// 安全：agent_id 永不展示，一律显示主机名（与后端 FrpConfigView/TargetAgent 同策略）。

import { useCallback, useEffect, useState } from 'react';
import {
  App,
  Button,
  Empty,
  Form,
  Input,
  Modal,
  Popconfirm,
  Select,
  Skeleton,
  Tag,
  Upload,
} from 'antd';
import type { UploadFile } from 'antd';
import { InboxOutlined, PlusOutlined } from '@ant-design/icons';
import { api } from '../api';
import type { Agent, AgentPlugin, BuiltinPluginGroup, Plugin } from '../types';
import { fmtBytes } from '../format';

/** 公网机（Server 同机 agent）哨兵：与后端 api/serverPathID 对齐。 */
const SERVER_TARGET = 'server';
const UNKNOWN_HOST = '未知节点';

function fmtTime(t?: string) {
  if (!t) return '—';
  const d = new Date(t);
  return Number.isNaN(d.getTime()) ? t : d.toLocaleString();
}

function shortSha(s: string) {
  return s ? s.slice(0, 12) : '—';
}

/** 单条插件版本行。 */
function VersionRow({
  p,
  isBuiltin,
  onAssign,
  onDelete,
}: {
  p: Plugin;
  isBuiltin: boolean; // 该 id 是否为内置插件（以各节点上报清单为准）
  onAssign: (p: Plugin) => void;
  onDelete: (p: Plugin) => void;
}) {
  return (
    <div
      className="flex flex-wrap items-center gap-x-4 gap-y-1 border-t px-1 py-2 text-sm"
      style={{ borderColor: 'var(--line)' }}
    >
      <Tag color="blue" className="num">{p.version}</Tag>
      {isBuiltin && <Tag color="gold">内置</Tag>}
      <span className="num text-xs" style={{ color: 'var(--mut)' }}>{fmtBytes(p.size)}</span>
      <span className="num text-xs" style={{ color: 'var(--mut)' }} title={p.sha256}>
        sha256 {shortSha(p.sha256)}
      </span>
      <span className="text-xs" style={{ color: 'var(--mut)' }}>{fmtTime(p.created_at)}</span>
      <div className="ml-auto flex items-center gap-2">
        <Button size="small" type="link" onClick={() => onAssign(p)}>指派</Button>
        <Popconfirm
          title={isBuiltin ? '删除该版本？内置插件随 agent 发布，删除本行不影响节点本地运行。' : '删除该版本？若仍被节点指派将拒绝删除。'}
          onConfirm={() => onDelete(p)}
        >
          <Button size="small" type="link" danger>删除</Button>
        </Popconfirm>
      </div>
    </div>
  );
}

/** 内置插件卡片：各节点上报的内置采集插件（host/docker/disk），Server 只读展示。
 *  已取消指派的节点在此行内提供「指派」按钮 —— 内置版本不在插件仓库，
 *  只能按节点上报的版本重新指派（可停用 / 可恢复的闭环）。 */
function BuiltinCard({
  groups,
  assigned,
  hostnameToAgentId,
  onAssign,
}: {
  groups: BuiltinPluginGroup[];
  assigned: Set<string>; // `${agent_id}/${plugin_id}` 已指派集合
  hostnameToAgentId: Map<string, string>;
  onAssign: (agentId: string, pluginId: string, version: string) => void;
}) {
  return (
    <div className="card p-5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium" style={{ color: 'var(--fg)' }}>内置插件</span>
        <Tag color="gold">随 agent 发布</Tag>
        <span className="text-xs" style={{ color: 'var(--mut)' }}>
          已自动指派到各节点（取消指派即停用）· 上传更高版本可升级，无需重新发版 agent
        </span>
      </div>
      {groups.length === 0 ? (
        <div className="mt-3 text-sm" style={{ color: 'var(--mut)' }}>
          暂无节点上报（节点下次心跳注册后出现）。
        </div>
      ) : (
        <div className="mt-3 space-y-3">
          {groups.map((g) => (
            <div key={g.plugin_id} className="border-t pt-3 text-sm" style={{ borderColor: 'var(--line)' }}>
              <div className="flex flex-wrap items-center gap-3">
                <span className="num font-medium" style={{ color: 'var(--fg)' }}>{g.plugin_id}</span>
                {g.versions.map((v) => (
                  <Tag key={v} color="blue" className="num">{v}</Tag>
                ))}
                <span className="text-xs" style={{ color: 'var(--mut)' }}>{g.node_count} 个节点</span>
              </div>
              <div className="mt-1.5 space-y-1">
                {g.nodes.map((n) => {
                  const agentId = hostnameToAgentId.get(n.hostname);
                  const isAssigned = !!agentId && assigned.has(`${agentId}/${g.plugin_id}`);
                  return (
                    <div key={`${n.hostname}@${n.version}`} className="flex flex-wrap items-center gap-3 text-xs">
                      <span className="num" style={{ color: 'var(--mut)' }}>
                        {n.hostname}
                        <span style={{ opacity: 0.7 }}> · {n.version} · sha {shortSha(n.sha256)}</span>
                      </span>
                      {isAssigned ? (
                        <Tag color="green">已指派</Tag>
                      ) : (
                        <Button
                          size="small"
                          type="link"
                          disabled={!agentId}
                          onClick={() => agentId && onAssign(agentId, g.plugin_id, n.version)}
                        >
                          指派
                        </Button>
                      )}
                    </div>
                  );
                })}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

export default function Plugins() {
  const { message } = App.useApp();
  const [rows, setRows] = useState<Plugin[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [assigns, setAssigns] = useState<AgentPlugin[]>([]); // 全部节点的指派（前端扇出聚合）
  const [builtins, setBuiltins] = useState<BuiltinPluginGroup[]>([]); // 各节点上报的内置插件清单
  const [loading, setLoading] = useState(false);

  const [uploadOpen, setUploadOpen] = useState(false);
  const [fileList, setFileList] = useState<UploadFile[]>([]);
  const [uploading, setUploading] = useState(false);
  const [form] = Form.useForm<Record<string, unknown>>();

  const [assignFor, setAssignFor] = useState<Plugin | null>(null);
  const [assignForm] = Form.useForm<Record<string, unknown>>();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [p, a, b] = await Promise.all([api.plugins(), api.agents(), api.builtins().catch(() => [])]);
      setRows(p);
      setAgents(a);
      setBuiltins(b);
      // 扇出各节点已指派插件（无「全部指派」端点；节点数少，成本可忽略）。
      // 公网机（server 哨兵）的指派不在 agents 列表里，须单独拉——否则公网机内置插件
      // 虽已播种指派却不出现在「已指派」，用户无从取消。
      const targets = [SERVER_TARGET, ...a.map((ag) => ag.agent_id)];
      const per = await Promise.all(
        targets.map((t) => api.agentPlugins(t).catch(() => [] as AgentPlugin[])),
      );
      setAssigns(per.flat());
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

  const hostnameOf = useCallback(
    (agentId: string) => {
      if (agentId === SERVER_TARGET || agentId === '') return '公网机（Server 同机）';
      return agents.find((a) => a.agent_id === agentId)?.hostname || UNKNOWN_HOST;
    },
    [agents],
  );

  const openUpload = () => {
    setFileList([]);
    form.resetFields();
    setUploadOpen(true);
  };

  const doUpload = async () => {
    const values = form.getFieldsValue();
    const file = fileList[0]?.originFileObj as File | undefined;
    if (!file) {
      message.error('请选择插件二进制文件');
      return;
    }
    setUploading(true);
    try {
      await api.uploadPlugin(file, {
        id: values.id as string,
        name: values.name as string,
        version: values.version as string,
        args_schema: (values.args_schema as string) ?? '',
      });
      message.success('插件版本已上传');
      setUploadOpen(false);
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    } finally {
      setUploading(false);
    }
  };

  const remove = async (p: Plugin) => {
    try {
      await api.deletePluginVersion(p.id, p.version);
      message.success('插件版本已删除');
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  const openAssign = (p: Plugin) => {
    setAssignFor(p);
    assignForm.resetFields();
    assignForm.setFieldsValue({ target: SERVER_TARGET, args_json: '' });
  };

  const doAssign = async () => {
    if (!assignFor) return;
    const v = assignForm.getFieldsValue();
    const target = v.target as string;
    try {
      await api.assignPlugin(assignFor.id, {
        agent_id: target === SERVER_TARGET ? '' : target,
        version: assignFor.version,
        args_json: (v.args_json as string) ?? '',
      });
      message.success('已指派，节点下个心跳拉取并执行');
      setAssignFor(null);
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  const unassign = async (ap: AgentPlugin) => {
    try {
      await api.unassignPlugin(ap.agent_id, ap.plugin_id);
      message.success('已取消指派，节点下个心跳停止该插件');
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  // 按 plugin id 分组（同 id 多版本）
  const groups = new Map<string, Plugin[]>();
  for (const p of rows) {
    const g = groups.get(p.id) ?? [];
    g.push(p);
    groups.set(p.id, g);
  }

  // 内置插件 id 集合（以各节点上报清单为准）：版本行与已指派行据此标注。
  const builtinIds = new Set(builtins.map((g) => g.plugin_id));
  // 已指派集合（内置卡片行内「指派/已指派」判定）与主机名→agent_id 反查（卡片只拿到主机名）。
  const assignedKeys = new Set(assigns.map((ap) => `${ap.agent_id}/${ap.plugin_id}`));
  const hostnameToAgentId = new Map(agents.map((a) => [a.hostname, a.agent_id]));

  // 重新指派内置插件（取消指派后恢复）：版本取节点上报值，Server 侧以内置清单校验。
  const assignBuiltin = async (agentId: string, pluginId: string, version: string) => {
    try {
      await api.assignPlugin(pluginId, { agent_id: agentId, version, args_json: '' });
      message.success('已指派，节点下个心跳恢复该内置插件');
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  const targetOptions = [
    { value: SERVER_TARGET, label: '公网机（Server 同机）' },
    ...agents.map((a) => ({ value: a.agent_id, label: a.hostname })),
  ];

  if (loading && rows.length === 0 && assigns.length === 0 && builtins.length === 0) {
    return <div className="card p-4"><Skeleton active paragraph={{ rows: 6 }} /></div>;
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <span className="text-xs" style={{ color: 'var(--mut)' }}>
          插件 = 不可变二进制版本（同 id 可并存多版本）· 指派后经 DesiredState 下发，节点下个心跳拉取执行
        </span>
        <Button type="primary" icon={<PlusOutlined />} onClick={openUpload}>上传插件</Button>
      </div>

      {/* 内置插件：随 agent 发布，Server 只读聚合各节点上报清单 */}
      <BuiltinCard
        groups={builtins}
        assigned={assignedKeys}
        hostnameToAgentId={hostnameToAgentId}
        onAssign={assignBuiltin}
      />

      {/* 已指派：按节点展示（主机名，不显示 agent_id） */}
      <div className="card p-5">
        <div className="mb-3 text-sm font-medium" style={{ color: 'var(--fg)' }}>已指派</div>
        {assigns.length === 0 ? (
          <div className="text-sm" style={{ color: 'var(--mut)' }}>暂无指派。上传插件后在版本行点「指派」选择节点。</div>
        ) : (
          <div className="space-y-2">
            {assigns.map((ap) => (
              <div key={`${ap.agent_id}/${ap.plugin_id}`} className="flex flex-wrap items-center gap-3 text-sm">
                <Tag color="blue">{hostnameOf(ap.agent_id)}</Tag>
                <span className="num">{ap.plugin_id}@{ap.version}</span>
                {builtinIds.has(ap.plugin_id) && <Tag color="gold">内置</Tag>}
                {ap.args_json && (
                  <span className="num text-xs" style={{ color: 'var(--mut)' }} title={ap.args_json}>{ap.args_json}</span>
                )}
                <div className="ml-auto">
                  <Button size="small" type="link" danger onClick={() => unassign(ap)}>取消指派</Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* 插件仓库：按 id 分组各版本 */}
      {rows.length === 0 ? (
        <div className="card p-10">
          <Empty description="插件仓库为空，点击右上「上传插件」添加" />
        </div>
      ) : (
        [...groups.entries()].map(([id, versions]) => (
          <div key={id} className="card p-5 text-sm" style={{ borderColor: 'var(--line)' }}>
            <div className="flex flex-wrap items-center gap-3">
              <span className="font-medium" style={{ color: 'var(--fg)' }}>{versions[0].name || id}</span>
              <span className="num text-xs" style={{ color: 'var(--mut)' }}>{id}</span>
              <Tag>{versions.length} 个版本</Tag>
            </div>
            <div className="mt-2">
              {versions.map((p) => (
                <VersionRow key={p.version} p={p} isBuiltin={builtinIds.has(id)} onAssign={openAssign} onDelete={remove} />
              ))}
            </div>
          </div>
        ))
      )}

      {/* 上传 Modal */}
      <Modal
        title="上传插件版本"
        open={uploadOpen}
        onOk={doUpload}
        onCancel={() => setUploadOpen(false)}
        okText="上传"
        cancelText="取消"
        confirmLoading={uploading}
        width={560}
      >
        <Form<Record<string, unknown>> form={form} layout="vertical" style={{ marginTop: 12 }}>
          <Form.Item label="插件二进制" required>
            <Upload.Dragger
              multiple={false}
              maxCount={1}
              fileList={fileList}
              beforeUpload={() => false}
              onChange={({ fileList: fl }) => setFileList(fl.slice(-1))}
            >
              <p className="ant-upload-drag-icon"><InboxOutlined /></p>
              <p className="ant-upload-text">点击或拖拽文件到此处</p>
              <p className="ant-upload-hint">release 构建的独立二进制（≤64MB）</p>
            </Upload.Dragger>
          </Form.Item>

          <div className="grid grid-cols-2 gap-3">
            <Form.Item name="id" label="插件标识" rules={[{ required: true, message: '请输入插件标识' }]} extra="稳定跨版本，如 hello">
              <Input placeholder="hello" />
            </Form.Item>
            <Form.Item name="name" label="展示名" rules={[{ required: true, message: '请输入展示名' }]}>
              <Input placeholder="例如：hello 探测" maxLength={64} />
            </Form.Item>
          </div>

          <Form.Item
            name="version"
            label="版本号"
            rules={[{ required: true, message: '请输入版本号' }]}
            extra="同 id 同版本不可重复；版本号仅作标识，无 semver 排序"
          >
            <Input placeholder="0.1.0" />
          </Form.Item>

          <Form.Item name="args_schema" label="参数说明（可选，JSON）">
            <Input.TextArea rows={2} placeholder='{"msg":"string"}' />
          </Form.Item>
        </Form>
      </Modal>

      {/* 指派 Modal */}
      <Modal
        title={assignFor ? `指派 ${assignFor.id}@${assignFor.version}` : '指派插件'}
        open={!!assignFor}
        onOk={doAssign}
        onCancel={() => setAssignFor(null)}
        okText="指派"
        cancelText="取消"
        width={520}
      >
        <Form<Record<string, unknown>> form={assignForm} layout="vertical" style={{ marginTop: 12 }}>
          <Form.Item name="target" label="目标节点" rules={[{ required: true, message: '请选择目标节点' }]}>
            <Select
              showSearch
              placeholder="选择节点"
              optionFilterProp="label"
              options={targetOptions}
            />
          </Form.Item>
          <Form.Item
            name="args_json"
            label="args_json（插件 run 参数，可选）"
            extra={'JSON 对象，如 {"msg":"ping"}'}
          >
            <Input.TextArea rows={3} placeholder='{"msg":"ping"}' />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
