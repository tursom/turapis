import { useState, useCallback, useRef } from 'react'
import { Tag, Button, Tooltip, Typography, Popconfirm, Select } from 'antd'
import { HolderOutlined, WarningOutlined, PlusOutlined, ThunderboltOutlined } from '@ant-design/icons'
import type { Provider } from '../../api/types'

const { Text } = Typography

interface DefaultChainBarProps {
  providers: Provider[]
  groups: Provider[][]
  onGroupsChange: (groups: Provider[][]) => void
}

export default function DefaultChainBar({ providers, groups, onGroupsChange }: DefaultChainBarProps) {
  const [dragInfo, setDragInfo] = useState<{ providerId: number; fromGroupIdx: number; fromProviderIdx: number } | null>(null)
  const [addingProvider, setAddingProvider] = useState(false)
  const [dropTargetGroup, setDropTargetGroup] = useState<number | null>(null)
  const addBtnRef = useRef<HTMLDivElement>(null)

  const availableProviders = providers.filter(
    (p) => !groups.some((g) => g.some((gp) => gp.id === p.id)),
  )

  const handleDragStart = (providerId: number, fromGroupIdx: number, fromProviderIdx: number) => {
    setDragInfo({ providerId, fromGroupIdx, fromProviderIdx })
  }

  const handleDragOverGroup = (e: React.DragEvent, groupIdx: number) => {
    e.preventDefault()
    e.dataTransfer.dropEffect = 'move'
    setDropTargetGroup(groupIdx)
  }

  const handleDragLeaveGroup = () => {
    setDropTargetGroup(null)
  }

  const handleDropOnGroup = (targetGroupIdx: number) => {
    setDropTargetGroup(null)
    if (!dragInfo) return

    const { fromGroupIdx, fromProviderIdx } = dragInfo
    if (fromGroupIdx === targetGroupIdx) {
      setDragInfo(null)
      return
    }

    const newGroups = groups.map((g) => [...g])
    const [moved] = newGroups[fromGroupIdx].splice(fromProviderIdx, 1)

    if (newGroups[fromGroupIdx].length === 0) {
      newGroups.splice(fromGroupIdx, 1)
      const actualTarget = targetGroupIdx > fromGroupIdx ? targetGroupIdx - 1 : targetGroupIdx
      newGroups[actualTarget].push(moved)
    } else {
      newGroups[targetGroupIdx].push(moved)
    }

    onGroupsChange(newGroups)
    setDragInfo(null)
  }

  const handleDropOnNewGroup = () => {
    if (!dragInfo) return

    const { fromGroupIdx, fromProviderIdx } = dragInfo
    const newGroups = groups.map((g) => [...g])
    const [moved] = newGroups[fromGroupIdx].splice(fromProviderIdx, 1)

    if (newGroups[fromGroupIdx].length === 0) {
      newGroups.splice(fromGroupIdx, 1)
    }

    newGroups.push([moved])
    onGroupsChange(newGroups)
    setDragInfo(null)
  }

  const handleDragEnd = () => {
    setDragInfo(null)
    setDropTargetGroup(null)
  }

  const handleRemoveProvider = useCallback(
    (groupIdx: number, providerIdx: number) => {
      const newGroups = groups.map((g) => [...g])
      newGroups[groupIdx].splice(providerIdx, 1)
      if (newGroups[groupIdx].length === 0) {
        newGroups.splice(groupIdx, 1)
      }
      onGroupsChange(newGroups)
    },
    [groups, onGroupsChange],
  )

  const handleAddProvider = (providerId: number) => {
    const provider = providers.find((p) => p.id === providerId)
    if (!provider) return
    onGroupsChange([...groups, [provider]])
    setAddingProvider(false)
  }

  if (groups.length === 0) {
    return (
      <div
        style={{ marginTop: 12, padding: 16, background: '#fffbe6', border: '1px solid #ffe58f', borderRadius: 8, flexShrink: 0 }}
        ref={addBtnRef}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
          <WarningOutlined style={{ color: '#faad14' }} />
          <Text type="secondary">尚未配置故障转移链。未匹配专属映射的请求将直接返回错误。</Text>
        </div>
        {addingProvider ? (
          <Select
            autoFocus
            size="small"
            style={{ minWidth: 200 }}
            placeholder="选择供应商..."
            options={availableProviders.map((p) => ({ value: p.id, label: p.name }))}
            onChange={(val) => handleAddProvider(val as number)}
            onBlur={() => setAddingProvider(false)}
          />
        ) : (
          <Button
            type="dashed"
            size="small"
            icon={<PlusOutlined />}
            onClick={() => setAddingProvider(true)}
            disabled={availableProviders.length === 0}
          >
            添加供应商到链
          </Button>
        )}
      </div>
    )
  }

  return (
    <div
      style={{ marginTop: 12, flexShrink: 0 }}
      onDragOver={(e) => {
        e.preventDefault()
        e.dataTransfer.dropEffect = 'move'
      }}
      onDrop={(e) => {
        e.preventDefault()
        handleDropOnNewGroup()
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
        <ThunderboltOutlined style={{ color: '#1677ff' }} />
        <Text strong style={{ fontSize: 13 }}>故障转移优先级配置</Text>
        <Text type="secondary" style={{ fontSize: 11 }}>
          同组共享优先级（并行），箭头方向依次故障转移
        </Text>
      </div>

      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 0,
          flexWrap: 'wrap',
          padding: '8px 0',
          minHeight: 56,
          background: dragInfo ? '#f0f5ff' : 'transparent',
          borderRadius: 8,
          transition: 'background 0.2s',
        }}
      >
        {groups.map((group, groupIdx) => (
          <div key={groupIdx} style={{ display: 'flex', alignItems: 'center' }}>
            <div
              onDragOver={(e) => handleDragOverGroup(e, groupIdx)}
              onDragLeave={handleDragLeaveGroup}
              onDrop={(e) => {
                e.preventDefault()
                e.stopPropagation()
                handleDropOnGroup(groupIdx)
              }}
              style={{
                display: 'flex',
                flexDirection: 'column',
                gap: 4,
                padding: '8px 10px',
                background: dropTargetGroup === groupIdx ? '#e6f4ff' : '#fafafa',
                border: `2px dashed ${dropTargetGroup === groupIdx ? '#1677ff' : '#d9d9d9'}`,
                borderRadius: 10,
                minWidth: 120,
                transition: 'all 0.2s',
              }}
            >
              <Tag
                color="blue"
                style={{
                  alignSelf: 'flex-start',
                  margin: 0,
                  fontSize: 11,
                  fontWeight: 700,
                  padding: '0 6px',
                }}
              >
                P{groupIdx + 1}
              </Tag>
              {group.map((provider, providerIdx) => (
                <Tooltip key={provider.id} title="拖拽到其他组可合并，拖到空白处可拆分">
                  <div
                    draggable
                    onDragStart={(e) => {
                      e.dataTransfer.effectAllowed = 'move'
                      e.stopPropagation()
                      handleDragStart(provider.id, groupIdx, providerIdx)
                    }}
                    onDragEnd={handleDragEnd}
                    style={{
                      padding: '4px 10px',
                      background: dragInfo?.providerId === provider.id ? '#e6f4ff' : '#fff',
                      border: `1px solid ${dragInfo?.providerId === provider.id ? '#1677ff' : provider.enabled ? '#b7eb8f' : '#d9d9d9'}`,
                      borderRadius: 6,
                      cursor: 'grab',
                      display: 'flex',
                      alignItems: 'center',
                      gap: 6,
                      userSelect: 'none',
                      opacity: dragInfo?.providerId === provider.id ? 0.5 : 1,
                      transition: 'all 0.15s',
                    }}
                  >
                    <HolderOutlined style={{ color: '#bfbfbf', fontSize: 10, flexShrink: 0 }} />
                    <Text style={{ fontSize: 12, fontWeight: 500, flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{provider.name}</Text>
                    <Tag
                      color={provider.auth_mode === 'oauth' ? 'cyan' : provider.protocol === 'openai' ? 'blue' : 'purple'}
                      style={{ margin: 0, fontSize: 9, lineHeight: '14px', padding: '0 3px', flexShrink: 0 }}
                    >
                      {provider.auth_mode === 'oauth' && provider.protocol === 'openai' ? 'Codex' : provider.protocol === 'openai' ? 'OpenAI' : 'Anthropic'}
                    </Tag>
                    <Tag color={provider.auth_mode === 'oauth' ? 'orange' : 'default'} style={{ margin: 0, fontSize: 9, lineHeight: '14px', padding: '0 3px', flexShrink: 0 }}>
                      {provider.auth_mode === 'oauth' ? 'OAuth' : 'API'}
                    </Tag>
                    {!provider.enabled && (
                      <Tag color="default" style={{ margin: 0, fontSize: 9, lineHeight: '14px', padding: '0 3px' }}>
                        禁用
                      </Tag>
                    )}
                    <Popconfirm
                      title="从链中移除？"
                      onConfirm={() => handleRemoveProvider(groupIdx, providerIdx)}
                      okText="移除"
                      cancelText="取消"
                    >
                      <span
                        style={{ fontSize: 14, color: '#bfbfbf', cursor: 'pointer', lineHeight: 1 }}
                        onClick={(e) => e.stopPropagation()}
                      >
                        ×
                      </span>
                    </Popconfirm>
                  </div>
                </Tooltip>
              ))}
            </div>
            {groupIdx < groups.length - 1 && (
              <div style={{ display: 'flex', alignItems: 'center', padding: '0 4px', flexShrink: 0 }}>
                <svg width="28" height="16" viewBox="0 0 28 16">
                  <defs>
                    <marker id={`arrowhead-${groupIdx}`} markerWidth="6" markerHeight="6" refX="5" refY="3" orient="auto">
                      <polygon points="0 0, 6 3, 0 6" fill="#1677ff" />
                    </marker>
                  </defs>
                  <line x1="4" y1="8" x2="22" y2="8" stroke="#1677ff" strokeWidth="2" markerEnd={`url(#arrowhead-${groupIdx})`} />
                </svg>
              </div>
            )}
          </div>
        ))}
        <div ref={addBtnRef} style={{ marginLeft: 8 }}>
          {addingProvider ? (
            <Select
              autoFocus
              size="small"
              style={{ minWidth: 160 }}
              placeholder="添加供应商..."
              options={availableProviders.map((p) => ({ value: p.id, label: p.name }))}
              onChange={(val) => handleAddProvider(val as number)}
              onBlur={() => setAddingProvider(false)}
            />
          ) : (
            <Button
              type="dashed"
              size="small"
              icon={<PlusOutlined />}
              onClick={() => setAddingProvider(true)}
              disabled={availableProviders.length === 0}
            >
              添加供应商
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}
