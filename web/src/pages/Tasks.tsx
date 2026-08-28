// 定时任务（阶段二 S4）：
//   任务 = cron + 目标节点 + 插件引用 + args，执行全在目标 agent 侧
//   （本地 cron 到期触发 → 插件一次性 run → 结果落 task_runs 审计）。
//   列表卡片（启用开关 / 最近运行摘要）+ 运行历史 Drawer + 新建/编辑 Modal。
// 安全：agent_id 永不展示，一律显示主机名；插件下拉只列目标节点已指派插件
//   （与 Server 校验同源：任务只能引用指派清单内插件）。
// 目标节点：含「公网机（Server 同机）」选项（哨兵 server，与后端 serverPathID 对齐），
//   保存时归一化为空串（后端解析为 settings.server_agent_id）。

import { useCallback, useEffect, useState } from 'react';
import {
  App,
  Button,
  Drawer,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Skeleton,
  Switch,
  Tag,
} from 'antd';
import { HistoryOutlined, PlusOutlined, ThunderboltOutlined } from '@ant-design/icons';
import { api } from '../api';
import type { Agent, AgentPlugin, Task, TaskRun } from '../types';

/** 公网机（Server 同机 agent）哨兵：与后端 api/serverPathID 对齐。 */
const SERVER_TARGET = 'server';

const STATUS_COLOR: Record<string, string> = {
  ok: 'green',
  failed: 'red',
  timeout: 'orange',
  skipped: 'default',
};

function statusTag(s: string) {
  return <Tag color={STATUS_COLOR[s] ?? 'default'}>{s || '—'}</Tag>;
}

function fmtTime(t?: string) {
  if (!t) return '—';
  const d = new Date(t);
  return Number.isNaN(d.getTime()) ? t : d.toLocaleString();
}

/** 运行状态列（最近运行摘要或历史）：status / 时间 / 退出码 / 输出尾部。 */
function RunSummary({ task }: { task: Task }) {
  if (!task.last_run_at && !task.last_status) {
    return <div className="mt-2 text-xs" style={{ color: 'var(--mut)' }}>尚未运行（下次 cron 触发后回显）</div>;
  }
  return (
    <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs" style={{ color: 'var(--mut)' }}>
      <span className="flex items-center gap-1.5">最近运行{statusTag(task.last_status)}{fmtTime(task.last_run_at)}</span>
      {task.last_output_tail && (
        <span className="num" title={task.last_output_tail} style={{ maxWidth: 480 }}>
          {task.last_output_tail.length > 120 ? `${task.last_output_tail.slice(0, 120)}…` : task.last_output_tail}
        </span>
      )}
    </div>
  );
}

export default function Tasks() {
  const { message } = App.useApp();
  const [rows, setRows] = useState<Task[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<Task | null>(null);
  const [runsTask, setRunsTask] = useState<Task | null>(null);
  const [runs, setRuns] = useState<TaskRun[]>([]);
  const [runningId, setRunningId] = useState<string | null>(null); // 立即运行请求中的任务
  const [pluginOpts, setPluginOpts] = useState<AgentPlugin[]>([]);
  const [form] = Form.useForm<Record<string, unknown>>();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [t, a] = await Promise.all([api.tasks(), api.agents()]);
      setRows(t);
      setAgents(a);
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

  // 目标节点变更 → 联动插件下拉（只列该节点已指派插件）
  const loadPlugins = useCallback(async (target: string) => {
    try {
      setPluginOpts(await api.agentPlugins(target));
    } catch {
      setPluginOpts([]);
    }
  }, []);

  const openNew = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({ target: SERVER_TARGET, enabled: true, timeout_s: 30 });
    loadPlugins(SERVER_TARGET);
    setModalOpen(true);
  };

  const openEdit = (t: Task) => {
    setEditing(t);
    form.resetFields();
    form.setFieldsValue({
      ...t,
      target: t.target_agent_id === '' ? SERVER_TARGET : t.target_agent_id,
      plugin_id: t.plugin_id,
    });
    loadPlugins(t.target_agent_id === '' ? SERVER_TARGET : t.target_agent_id);
    setModalOpen(true);
  };

  const save = async () => {
    const values = form.getFieldsValue();
    const target = values.target as string;
    const body = {
      name: values.name as string,
      description: (values.description as string) ?? '',
      target_agent_id: target === SERVER_TARGET ? '' : target,
      cron: values.cron as string,
      plugin_id: values.plugin_id as string,
      args_json: (values.args_json as string) ?? '',
      timeout_s: (values.timeout_s as number) ?? 30,
      enabled: (values.enabled as boolean) ?? true,
    };
    try {
      if (editing) {
        await api.updateTask(editing.id, body);
        message.success('任务已更新，节点下次心跳对齐新定义');
      } else {
        await api.createTask(body);
        message.success('任务已创建，节点按 cron 定时触发');
      }
      setModalOpen(false);
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  const remove = async (t: Task) => {
    try {
      await api.deleteTask(t.id);
      message.success('任务已删除，节点下次心跳停止调度');
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  const toggle = async (t: Task, enabled: boolean) => {
    try {
      await api.updateTask(t.id, {
        name: t.name,
        description: t.description,
        target_agent_id: t.target_agent_id,
        cron: t.cron,
        plugin_id: t.plugin_id,
        args_json: t.args_json,
        timeout_s: t.timeout_s,
        enabled,
      });
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  const runNow = async (t: Task) => {
    setRunningId(t.id);
    try {
      await api.runTask(t.id);
      message.success('已触发，目标节点下个心跳立即执行一次');
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    } finally {
      setRunningId(null);
    }
  };

  const openRuns = async (t: Task) => {
    setRunsTask(t);
    setRuns([]);
    try {
      setRuns(await api.taskRuns(t.id, 50));
    } catch {
      setRuns([]);
    }
  };

  const targetOptions = [
    { value: SERVER_TARGET, label: '公网机（Server 同机）' },
    ...agents.map((a) => ({ value: a.agent_id, label: a.hostname })),
  ];
  const pluginOptions = pluginOpts.map((p) => ({ value: p.plugin_id, label: `${p.plugin_id}@${p.version}` }));

  if (loading && rows.length === 0) {
    return <div className="card p-4"><Skeleton active paragraph={{ rows: 6 }} /></div>;
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <span className="text-xs" style={{ color: 'var(--mut)' }}>
          任务 = cron + 目标节点 + 插件 + args · 到期由节点本地触发插件一次性 run，结果落运行历史
        </span>
        <Button type="primary" icon={<PlusOutlined />} onClick={openNew}>新建任务</Button>
      </div>

      {rows.length === 0 ? (
        <div className="card p-10 text-center text-sm" style={{ color: 'var(--mut)' }}>
          暂无定时任务。点击「新建任务」：选择目标节点与已指派插件，cron 到期自动触发。
        </div>
      ) : (
        rows.map((t) => (
          <div key={t.id} className="card p-5 text-sm" style={{ borderColor: 'var(--line)' }}>
            <div className="flex flex-wrap items-center gap-3">
              <span className="font-medium" style={{ color: 'var(--fg)' }}>{t.name}</span>
              <Tag color="blue">{t.target_agent_name}</Tag>
              <span className="num text-xs" style={{ color: 'var(--mut)' }}>{t.cron}</span>
              <Tag>{t.plugin_id}</Tag>
              {t.timeout_s > 0 && (
                <span className="text-xs num" style={{ color: 'var(--mut)' }}>超时 {t.timeout_s}s</span>
              )}
              <div className="ml-auto flex items-center gap-3">
                <Switch size="small" checked={t.enabled} onChange={(c) => toggle(t, c)} />
                <Button
                  size="small"
                  type="link"
                  icon={<ThunderboltOutlined />}
                  disabled={!t.enabled || t.run_now}
                  loading={runningId === t.id}
                  onClick={() => runNow(t)}
                >
                  {t.run_now ? '已触发' : '立即运行'}
                </Button>
                <Button size="small" type="link" onClick={() => openEdit(t)}>编辑</Button>
                <Button size="small" type="link" icon={<HistoryOutlined />} onClick={() => openRuns(t)}>
                  运行记录
                </Button>
                <Popconfirm title="删除后节点将停止调度该任务，确认？" onConfirm={() => remove(t)}>
                  <Button size="small" type="link" danger>删除</Button>
                </Popconfirm>
              </div>
            </div>
            {t.description && (
              <div className="mt-1 text-xs" style={{ color: 'var(--mut)' }}>{t.description}</div>
            )}
            <RunSummary task={t} />
          </div>
        ))
      )}

      <Modal
        title={editing ? '编辑定时任务' : '新建定时任务'}
        open={modalOpen}
        onOk={save}
        onCancel={() => setModalOpen(false)}
        okText="保存"
        cancelText="取消"
        width={560}
      >
        <Form<Record<string, unknown>> form={form} layout="vertical" style={{ marginTop: 12 }}>
          <div className="grid grid-cols-2 gap-3">
            <Form.Item name="name" label="任务名称" rules={[{ required: true, message: '请输入任务名称' }]}>
              <Input placeholder="例如：hello 定时探测" maxLength={64} />
            </Form.Item>
            <Form.Item name="target" label="目标节点" rules={[{ required: true, message: '请选择目标节点' }]}>
              <Select
                showSearch
                placeholder="选择节点"
                optionFilterProp="label"
                options={targetOptions}
                onChange={(v) => loadPlugins(String(v))}
              />
            </Form.Item>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <Form.Item
              name="cron"
              label="cron（分 时 日 月 周）"
              rules={[{ required: true, message: '请输入 cron 表达式' }]}
              extra="例如每分钟：* * * * *"
            >
              <Input placeholder="* * * * *" />
            </Form.Item>
            <Form.Item
              name="plugin_id"
              label="插件（目标节点已指派）"
              rules={[{ required: true, message: '请选择插件' }]}
            >
              <Select placeholder="选择插件" options={pluginOptions} />
            </Form.Item>
          </div>

          <div className="grid grid-cols-[1fr_120px] gap-3">
            <Form.Item
              name="args_json"
              label="args_json（插件 run 参数）"
              extra={'JSON 对象，如 {"msg":"ping"}'}
            >
              <Input.TextArea rows={3} placeholder='{"msg":"ping"}' />
            </Form.Item>
            <Form.Item name="timeout_s" label="超时（秒）" rules={[{ required: true }]}>
              <InputNumber style={{ width: '100%' }} min={1} max={86400} />
            </Form.Item>
          </div>

          <Form.Item name="description" label="描述（可选）">
            <Input.TextArea rows={1} placeholder="用途说明" maxLength={200} />
          </Form.Item>

          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>

      <Drawer
        title={runsTask ? `运行记录 · ${runsTask.name}` : '运行记录'}
        open={!!runsTask}
        onClose={() => setRunsTask(null)}
        width={640}
      >
        {runs.length === 0 ? (
          <div className="text-sm" style={{ color: 'var(--mut)' }}>暂无运行记录（等待 cron 触发）</div>
        ) : (
          <div className="space-y-3">
            {runs.map((r) => (
              <div key={r.id} className="rounded-lg border p-3 text-sm" style={{ borderColor: 'var(--line)' }}>
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs" style={{ color: 'var(--mut)' }}>
                  {statusTag(r.status)}
                  <span>{fmtTime(r.started_at)}</span>
                  <span className="num">exit {r.exit_code ?? '—'}</span>
                  <span className="num">耗时 {r.finished_at && r.started_at ? Math.max(0, (new Date(r.finished_at).getTime() - new Date(r.started_at).getTime()) / 1000).toFixed(1) : '—'}s</span>
                </div>
                {r.output && (
                  <pre
                    className="mt-2 whitespace-pre-wrap break-words rounded bg-black/5 p-2 text-xs"
                    style={{ color: 'var(--fg)', maxHeight: 160, overflow: 'auto' }}
                  >
                    {r.output}
                  </pre>
                )}
              </div>
            ))}
          </div>
        )}
      </Drawer>
    </div>
  );
}
