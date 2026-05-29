import { useState, useEffect } from 'react'
import { Table, Button, Modal, Form, Input, InputNumber, message, Tag } from 'antd'
import { EditOutlined } from '@ant-design/icons'
import { fetchConfig, updateConfigSetting } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import type { ConfigEntry } from '../api/types'

const SETTINGS_META: Record<string, { type: ConfigEntry['type']; description: string; editable: boolean }> = {
  schema_version: {
    type: 'string',
    description: '数据库 schema 版本（由迁移系统自动管理）',
    editable: false,
  },
  codex_cli_version: {
    type: 'string',
    description: 'Codex CLI 版本（由网关自动追踪）',
    editable: false,
  },
  default_priority_chain: {
    type: 'json',
    description: '默认模型优先级链配置，控制故障转移顺序',
    editable: true,
  },
  failover_error_cooldown_growth_seconds: {
    type: 'number',
    description: '故障转移冷却时间增长基数（秒），0 表示不增长',
    editable: true,
  },
}

export default function Config() {
  const { role } = useAuth()
  const isAdmin = role === 'admin'
  const [config, setConfig] = useState<ConfigEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [modalOpen, setModalOpen] = useState(false)
  const [editingEntry, setEditingEntry] = useState<ConfigEntry | null>(null)
  const [form] = Form.useForm()

  const loadConfig = async () => {
    setLoading(true)
    try {
      const data = await fetchConfig()
      const merged: ConfigEntry[] = Object.entries(SETTINGS_META).map(([key, meta]) => ({
        key,
        value: data[key] ?? '',
        ...meta,
      }))
      setConfig(merged)
    } catch {
      message.error('加载配置失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadConfig()
  }, [])

  const openEdit = (entry: ConfigEntry) => {
    setEditingEntry(entry)
    form.setFieldsValue({ value: entry.type === 'number' ? Number(entry.value) || 0 : entry.value })
    setModalOpen(true)
  }

  const handleSubmit = async () => {
    try {
      const { value } = await form.validateFields()
      const stringValue = String(value)
      await updateConfigSetting(editingEntry!.key, stringValue)
      message.success(`设置 ${editingEntry!.key} 已更新`)
      setModalOpen(false)
      loadConfig()
    } catch (err) {
      if (err instanceof Error) message.error(err.message)
    }
  }

  const columns = [
    {
      title: '设置项',
      dataIndex: 'key',
      key: 'key',
      width: 300,
    },
    {
      title: '当前值',
      dataIndex: 'value',
      key: 'value',
      render: (v: string, r: ConfigEntry) => {
        if (!v) return <span style={{ color: '#bfbfbf' }}>（空）</span>
        if (r.type === 'json') {
          const display = v.length > 60 ? v.substring(0, 60) + '...' : v
          return <Tag>{display}</Tag>
        }
        return <span>{v}</span>
      },
    },
    {
      title: '类型',
      dataIndex: 'type',
      key: 'type',
      width: 80,
      render: (t: string) => <Tag>{t}</Tag>,
    },
    {
      title: '说明',
      dataIndex: 'description',
      key: 'description',
    },
    {
      title: '操作',
      key: 'actions',
      width: 80,
      render: (_: unknown, record: ConfigEntry) =>
        record.editable && isAdmin ? (
          <Button type="link" icon={<EditOutlined />} onClick={() => openEdit(record)}>
            编辑
          </Button>
        ) : null,
    },
  ]

  const renderInput = () => {
    if (!editingEntry) return null
    switch (editingEntry.type) {
      case 'number':
        return <InputNumber style={{ width: '100%' }} min={0} />
      case 'json':
        return <Input.TextArea rows={8} />
      default:
        return <Input />
    }
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <h2 style={{ margin: 0 }}>全局配置</h2>
      </div>

      <Table dataSource={config} columns={columns} rowKey="key" loading={loading} pagination={false} />

      <Modal
        title={`编辑设置: ${editingEntry?.key ?? ''}`}
        open={modalOpen}
        onOk={handleSubmit}
        onCancel={() => setModalOpen(false)}
        destroyOnClose
        width={640}
      >
        {editingEntry && (
          <div style={{ marginBottom: 12, color: '#888' }}>{editingEntry.description}</div>
        )}
        <Form form={form} layout="vertical" style={{ marginTop: 8 }}>
          <Form.Item name="value" label="值" rules={[{ required: true, message: '请输入值' }]}>
            {renderInput()}
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}
