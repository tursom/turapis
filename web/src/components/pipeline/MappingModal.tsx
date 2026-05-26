import { useState, useEffect } from 'react'
import { Modal, Form, Select, InputNumber, Switch, message, AutoComplete } from 'antd'
import type { Provider, ModelMapping } from '../../api/types'

interface MappingModalProps {
  open: boolean
  editing: ModelMapping | null
  providers: Provider[]
  modelNames: string[]
  onClose: () => void
  onSave: (data: { model_name: string; provider_id: number; priority: number; enabled: boolean; id?: number }) => void
}

export default function MappingModal({ open, editing, providers, modelNames, onClose, onSave }: MappingModalProps) {
  const [modelName, setModelName] = useState('')
  const [providerId, setProviderId] = useState<number | null>(null)
  const [priority, setPriority] = useState(100)
  const [enabled, setEnabled] = useState(true)
  const [validating, setValidating] = useState(false)

  useEffect(() => {
    if (open) {
      if (editing) {
        setModelName(editing.model_name)
        setProviderId(editing.provider_id)
        setPriority(editing.priority)
        setEnabled(editing.enabled)
      } else {
        setModelName('')
        setProviderId(providers[0]?.id ?? null)
        setPriority(100)
        setEnabled(true)
      }
    }
  }, [open, editing, providers])

  const handleSubmit = () => {
    if (!modelName.trim()) {
      message.warning('请输入模型名称')
      return
    }
    if (providerId === null) {
      message.warning('请选择供应商')
      return
    }
    setValidating(true)
    onSave({
      model_name: modelName.trim(),
      provider_id: providerId,
      priority,
      enabled,
      ...(editing ? { id: editing.id } : {}),
    })
    setValidating(false)
  }

  const providerOptions = providers.map((p) => ({
    value: p.id,
    label: `#${p.id} ${p.name} (${p.protocol === 'openai' ? 'OpenAI' : p.protocol === 'codex' ? 'Codex' : 'Anthropic'})`,
  }))

  return (
    <Modal
      title={editing ? '编辑映射' : '新建映射'}
      open={open}
      onCancel={onClose}
      onOk={handleSubmit}
      okText={editing ? '保存' : '创建'}
      cancelText="取消"
      confirmLoading={validating}
      destroyOnClose
    >
      <Form layout="vertical" style={{ marginTop: 12 }}>
        <Form.Item label="模型名称" required>
          <AutoComplete
            value={modelName}
            onChange={setModelName}
            options={modelNames.filter((n) => n !== modelName).map((n) => ({ value: n }))}
            placeholder="例如: gpt-4o-mini"
            style={{ width: '100%' }}
            filterOption={(inputValue, option) =>
              option?.value?.toLowerCase().includes(inputValue.toLowerCase()) ?? false
            }
          />
        </Form.Item>
        <Form.Item label="供应商" required>
          <Select
            value={providerId}
            onChange={(val) => setProviderId(val as number)}
            options={providerOptions}
            placeholder="选择供应商"
            style={{ width: '100%' }}
            showSearch
            optionFilterProp="label"
          />
        </Form.Item>
        <Form.Item label="优先级">
          <InputNumber
            value={priority}
            onChange={(val) => setPriority(val ?? 100)}
            min={1}
            max={9999}
            style={{ width: '100%' }}
          />
        </Form.Item>
        <Form.Item label="启用">
          <Switch checked={enabled} onChange={setEnabled} />
        </Form.Item>
      </Form>
    </Modal>
  )
}
