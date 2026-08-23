// 告警规则：列表 + 新建/编辑 Modal + 删除 + 启用开关。
// 表格/表单/弹窗用 antd。

import { useCallback, useEffect, useState } from 'react';
import { App, Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Segmented, Skeleton, Switch, Tooltip } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { api } from '../api';
import { METRIC_LABELS } from '../constants';
import type { AlertRule } from '../types';
import { SeverityTag } from '../components/ui';

const METRIC_OPTIONS = Object.entries(METRIC_LABELS).map(([value, label]) => ({ value, label }));

export default function AlertRules() {
  const { message } = App.useApp();
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<AlertRule | null>(null);
  const [form] = Form.useForm<AlertRule>();
  // 0/1 布尔型指标（节点离线/容器停止）：阈值需小于 1 才能触发
  const metricWatch = Form.useWatch('metric', form);
  const isBoolMetric = metricWatch === 'offline' || metricWatch === 'container_down';

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setRules(await api.rules());
    } catch {
      /* 网络/服务异常静默，保留旧数据 */
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, 30_000);
    return () => clearInterval(t);
  }, [load]);

  const openNew = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({ metric: 'cpu_pct', op: '>', threshold: 80, severity: 'warning', duration_s: 0, enabled: true });
    setModalOpen(true);
  };

  const openEdit = (r: AlertRule) => {
    setEditing(r);
    form.setFieldsValue({ ...r });
    setModalOpen(true);
  };

  const save = async () => {
    const values = await form.validateFields();
    try {
      if (editing) await api.updateRule(editing.id, values);
      else await api.createRule(values);
      message.success(editing ? '规则已更新' : '规则已创建');
      setModalOpen(false);
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  const remove = async (id: string) => {
    try {
      await api.deleteRule(id);
      message.success('规则已删除');
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  const toggle = async (r: AlertRule, enabled: boolean) => {
    try {
      await api.updateRule(r.id, { ...r, enabled });
      load();
    } catch (e) {
      message.error(String(e).replace(/^Error:\s*/, ''));
    }
  };

  if (loading && rules.length === 0) {
    return <div className="card p-4"><Skeleton active paragraph={{ rows: 6 }} /></div>;
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <span className="text-xs" style={{ color: 'var(--mut)' }}>
          评估周期 60s · 持续超阈值达「持续秒」才触发 · 通知冷却 30 分钟
        </span>
        <Button type="primary" icon={<PlusOutlined />} onClick={openNew}>新建规则</Button>
      </div>

      {rules.length === 0 ? (
        <div className="card p-10 text-center text-sm" style={{ color: 'var(--mut)' }}>
          暂无告警规则。点击「新建规则」创建第一条。
        </div>
      ) : (
        <div className="card divide-y" style={{ borderColor: 'var(--line)' }}>
          {rules.map((r) => (
            <div key={r.id} className="flex items-center gap-3 px-5 py-3.5 text-sm">
              <span className="min-w-0 flex-1">
                <span className="block truncate font-medium" style={{ color: 'var(--fg)' }}>{r.name}</span>
                <span className="num mt-0.5 block text-xs" style={{ color: 'var(--mut)' }}>
                  {METRIC_LABELS[r.metric] ?? r.metric} · {r.op} {r.threshold} · {r.duration_s > 0 ? `${r.duration_s}s` : '立即'}
                </span>
              </span>
              <SeverityTag severity={r.severity} />
              <Tooltip title={r.enabled ? '已启用' : '已停用'}>
                <Switch size="small" checked={r.enabled} onChange={(c) => toggle(r, c)} />
              </Tooltip>
              <div className="flex items-center gap-1">
                <Button size="small" type="link" onClick={() => openEdit(r)}>编辑</Button>
                <Popconfirm title="确认删除该规则？" onConfirm={() => remove(r.id)}>
                  <Button size="small" type="link" danger>删除</Button>
                </Popconfirm>
              </div>
            </div>
          ))}
        </div>
      )}

      <Modal
        title={editing ? '编辑规则' : '新建规则'}
        open={modalOpen}
        onOk={save}
        onCancel={() => setModalOpen(false)}
        okText="保存"
        cancelText="取消"
        width={480}
      >
        <Form<AlertRule> form={form} layout="vertical" style={{ marginTop: 12 }}>
          <Form.Item name="name" label="规则名称" rules={[{ required: true, message: '请输入名称' }]}>
            <Input placeholder="例如：CPU 超过 80%" />
          </Form.Item>
          <Form.Item name="metric" label="指标" rules={[{ required: true }]}>
            <Select
              options={METRIC_OPTIONS}
              placeholder="选择指标"
              onChange={(v) => {
                const m = v as string;
                if (m === 'offline' || m === 'container_down') {
                  // 0/1 状态值：阈值 <1 才可能在“离线/停止”时触发
                  form.setFieldsValue({ threshold: 0.5, duration_s: 0 });
                } else if (isBoolMetric) {
                  form.setFieldsValue({ threshold: 80 });
                }
              }}
            />
          </Form.Item>
          <div className="grid grid-cols-3 gap-3">
            <Form.Item name="op" label="比较" rules={[{ required: true }]}>
              <Segmented options={[{ value: '>', label: '>' }, { value: '<', label: '<' }]} />
            </Form.Item>
            <Form.Item name="threshold" label="阈值" rules={[{ required: true, message: '请输入阈值' }]}>
              <InputNumber style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="duration_s" label="持续秒数">
              <InputNumber style={{ width: '100%' }} min={0} placeholder="0 = 立即" />
            </Form.Item>
          </div>
          {isBoolMetric && (
            <div className="-mt-1 mb-3 text-xs" style={{ color: 'var(--mut)' }}>
              提示：{metricWatch === 'offline' ? '节点离线' : '容器停止'} 指标取值为 0/1（{metricWatch === 'offline' ? '在线/离线' : '运行/停止'}），阈值填 0.5 即可
            </div>
          )}
          <div className="grid grid-cols-2 gap-3">
            <Form.Item name="severity" label="级别" rules={[{ required: true }]}>
              <Segmented options={[{ value: 'warning', label: '警告' }, { value: 'critical', label: '严重' }]} />
            </Form.Item>
            <Form.Item name="enabled" label="启用" valuePropName="checked">
              <Switch />
            </Form.Item>
          </div>
        </Form>
      </Modal>
    </div>
  );
}
