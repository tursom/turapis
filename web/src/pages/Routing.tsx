import { useState, useEffect, useCallback, useMemo } from 'react'
import { Card, Button, Space, message, Alert, Input, Tag } from 'antd'
import { PlusOutlined, ReloadOutlined, SearchOutlined, ArrowLeftOutlined } from '@ant-design/icons'
import {
  fetchModelMappings,
  createModelMapping,
  updateModelMapping,
  deleteModelMapping,
  fetchProviders,
  fetchSettings,
  updateSettings,
} from '../api/client'
import type { ModelMapping, Provider } from '../api/types'
import { PipelineCanvas, DefaultChainBar, MappingModal } from '../components/pipeline'

export default function Routing() {
  const [providers, setProviders] = useState<Provider[]>([])
  const [mappings, setMappings] = useState<ModelMapping[]>([])
  const [chainGroups, setChainGroups] = useState<Provider[][]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [modalOpen, setModalOpen] = useState(false)
  const [editingMapping, setEditingMapping] = useState<ModelMapping | null>(null)
  const [filterModel, setFilterModel] = useState('')
  const [focusedModel, setFocusedModel] = useState<string | null>(null)
  const loadData = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [providersData, mappingsData, settingsData] = await Promise.all([
        fetchProviders(),
        fetchModelMappings(),
        fetchSettings(),
      ])
      setProviders(providersData)
      setMappings(mappingsData)

      let parsedGroups: Provider[][] = []
      try {
        const raw = JSON.parse(settingsData.default_priority_chain)
        if (Array.isArray(raw) && raw.length > 0) {
          if (Array.isArray(raw[0])) {
            // 新格式: [[provider_name, ...], ...]
            parsedGroups = (raw as string[][]).map((group) =>
              group
                .map((name) => providersData.find((p) => p.name === name))
                .filter((p): p is Provider => p !== undefined),
            ).filter((g) => g.length > 0)
          } else if (typeof raw[0] === 'number') {
            // 旧格式: [provider_id, ...]
            parsedGroups = (raw as number[])
              .map((id) => providersData.find((p) => p.id === id))
              .filter((p): p is Provider => p !== undefined)
              .map((p) => [p])
          }
        }
      } catch {
        parsedGroups = []
      }
      setChainGroups(parsedGroups)
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : '加载数据失败'
      setError(msg)
      message.error(msg)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    loadData()
  }, [loadData])

  const handleCreateMapping = useCallback(async (modelName: string, providerId: number) => {
    try {
      await createModelMapping({
        model_name: modelName,
        provider_id: providerId,
        priority: 100,
        enabled: true,
      })
      message.success(`已创建映射：${modelName} → ${providers.find((p) => p.id === providerId)?.name ?? `#${providerId}`}`)
      const refreshed = await fetchModelMappings()
      setMappings(refreshed)
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : '创建映射失败'
      message.error(msg)
    }
  }, [providers])

  const handleSaveMapping = useCallback(async (data: {
    model_name: string
    provider_id: number
    priority: number
    enabled: boolean
    id?: number
  }) => {
    try {
      if (data.id) {
        const existing = mappings.find((m) => m.id === data.id)
        if (!existing) {
          message.error('映射不存在')
          return
        }
        await updateModelMapping({
          ...existing,
          model_name: data.model_name,
          provider_id: data.provider_id,
          priority: data.priority,
          enabled: data.enabled,
        })
        message.success(`已更新映射：${data.model_name}`)
      } else {
        await createModelMapping({
          model_name: data.model_name,
          provider_id: data.provider_id,
          priority: data.priority,
          enabled: data.enabled,
        })
        message.success(`已创建映射：${data.model_name}`)
      }
      setModalOpen(false)
      const refreshed = await fetchModelMappings()
      setMappings(refreshed)
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : '保存映射失败'
      message.error(msg)
    }
  }, [mappings])

  const handleEditMapping = useCallback((mapping: ModelMapping) => {
    setEditingMapping(mapping)
    setModalOpen(true)
  }, [])

  const handleDeleteMapping = useCallback(async (id: number) => {
    try {
      await deleteModelMapping(id)
      message.success('已删除映射')
      const refreshed = await fetchModelMappings()
      setMappings(refreshed)
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : '删除映射失败'
      message.error(msg)
    }
  }, [])

  const handleChainChange = useCallback(async (newGroups: Provider[][]) => {
    try {
      const serialized = newGroups.map((g) => g.map((p) => p.name))
      await updateSettings(JSON.stringify(serialized))
      setChainGroups(newGroups)
      message.success('故障转移链已更新')
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : '更新故障转移链失败'
      message.error(msg)
    }
  }, [])

  const handlePerModelChainChange = useCallback(async (modelName: string, newGroups: Provider[][]) => {
    try {
      const existingMappings = mappings.filter((m) => m.model_name === modelName)
      const providerIdsInGroups = new Set(newGroups.flat().map((p) => p.id))

      for (const m of existingMappings) {
        if (!providerIdsInGroups.has(m.provider_id)) {
          await deleteModelMapping(m.id)
        }
      }

      for (let gi = 0; gi < newGroups.length; gi++) {
        const group = newGroups[gi]
        const basePriority = (gi + 1) * 10
        for (let pi = 0; pi < group.length; pi++) {
          const provider = group[pi]
          const existing = existingMappings.find(
            (m) => m.provider_id === provider.id,
          )
          const priority = basePriority + pi
          if (existing) {
            if (existing.priority !== priority) {
              await updateModelMapping({ ...existing, priority })
            }
          } else {
            await createModelMapping({
              model_name: modelName,
              provider_id: provider.id,
              priority,
              enabled: true,
            })
          }
        }
      }

      const refreshed = await fetchModelMappings()
      setMappings(refreshed)
      message.success(`${modelName} 故障转移链已更新`)
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : '更新模型链失败'
      message.error(msg)
    }
  }, [mappings])

  const handleOpenCreateModal = useCallback(() => {
    setEditingMapping(null)
    setModalOpen(true)
  }, [])

  const modelNames = useMemo(() => [...new Set(mappings.map((m) => m.model_name))].sort(), [mappings])

  const chainGroupsForDisplay = chainGroups.length > 0
    ? chainGroups
    : providers.length > 0
      ? [providers.sort((a, b) => a.priority - b.priority)]
      : []

  const perModelGroups = useMemo(() => {
    if (!focusedModel) return []
    const modelMappings = mappings
      .filter((m) => m.model_name === focusedModel && m.enabled)
      .sort((a, b) => a.priority - b.priority)

    const groups: Provider[][] = []
    let currentGroup: Provider[] = []
    let lastPriority = -1

    for (const m of modelMappings) {
      const provider = providers.find((p) => p.id === m.provider_id)
      if (!provider) continue
      const groupBucket = Math.floor(m.priority / 10)
      if (groupBucket !== lastPriority && currentGroup.length > 0) {
        groups.push(currentGroup)
        currentGroup = []
      }
      currentGroup.push(provider)
      lastPriority = groupBucket
    }
    if (currentGroup.length > 0) groups.push(currentGroup)
    return groups
  }, [focusedModel, mappings, providers])

  if (error && !loading) {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', flex: 1, height: '100%', minHeight: 0 }}>
        <Card title="路由配置 - 可视化管道" extra={<Button icon={<ReloadOutlined />} onClick={loadData}>重试</Button>}>
          <Alert type="error" message={error} showIcon />
        </Card>
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', flex: 1, height: '100%', minHeight: 0 }}>
      <Card
        title={
          focusedModel ? (
            <Space>
              <Button
                type="text"
                icon={<ArrowLeftOutlined />}
                onClick={() => { setFocusedModel(null); setFilterModel('') }}
              />
              <span>路由配置</span>
              <Tag color="blue">{focusedModel}</Tag>
            </Space>
          ) : (
            '路由配置 - 可视化管道'
          )
        }
        style={{ flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0 }}
        styles={{ body: { flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0, overflow: 'hidden' } }}
        extra={
          <Space wrap>
            {!focusedModel && (
              <Input
                prefix={<SearchOutlined />}
                placeholder="搜索模型..."
                value={filterModel}
                onChange={(e) => setFilterModel(e.target.value)}
                allowClear
                style={{ width: 180 }}
              />
            )}
            <Button icon={<ReloadOutlined />} onClick={loadData} loading={loading}>
              刷新
            </Button>
            <Button type="primary" icon={<PlusOutlined />} onClick={handleOpenCreateModal} disabled={providers.length === 0}>
              添加映射
            </Button>
          </Space>
        }
      >
        <PipelineCanvas
          providers={providers}
          mappings={mappings}
          loading={loading}
          filterModel={filterModel}
          focusedModel={focusedModel}
          onFocusModel={setFocusedModel}
          onCreateMapping={handleCreateMapping}
          onEditMapping={handleEditMapping}
          onDeleteMapping={handleDeleteMapping}
        />
        {focusedModel ? (
          <DefaultChainBar
            providers={providers}
            groups={perModelGroups}
            onGroupsChange={(newGroups) => handlePerModelChainChange(focusedModel, newGroups)}
          />
        ) : (
          <DefaultChainBar
            providers={providers}
            groups={chainGroupsForDisplay}
            onGroupsChange={handleChainChange}
          />
        )}
      </Card>

      <MappingModal
        open={modalOpen}
        editing={editingMapping}
        providers={providers}
        modelNames={modelNames}
        onClose={() => setModalOpen(false)}
        onSave={handleSaveMapping}
      />
    </div>
  )
}
