import { useState, useCallback, useRef } from 'react'
import { Tag, Button, Tooltip, Typography, Popconfirm, Select } from 'antd'
import { HolderOutlined, WarningOutlined, PlusOutlined, ThunderboltOutlined, MenuOutlined } from '@ant-design/icons'
import type { Provider } from '../../api/types'

const { Text } = Typography

type DragPayload =
  | { type: 'provider'; providerId: number; fromGroupIdx: number; fromProviderIdx: number }
  | { type: 'group'; fromGroupIdx: number }

interface DefaultChainBarProps {
  providers: Provider[]
  groups: Provider[][]
  onGroupsChange: (groups: Provider[][]) => void
}

export default function DefaultChainBar({ providers, groups, onGroupsChange }: DefaultChainBarProps) {
  const [dragInfo, setDragInfo] = useState<DragPayload | null>(null)
  const [addingProvider, setAddingProvider] = useState(false)
  const [dropTargetGroup, setDropTargetGroup] = useState<number | null>(null)
  const addBtnRef = useRef<HTMLDivElement>(null)

  const availableProviders = providers.filter(
    (p) => !groups.some((g) => g.some((gp) => gp.id === p.id)),
  )

  const handleProviderDragStart = (providerId: number, fromGroupIdx: number, fromProviderIdx: number) => {
    setDragInfo({ type: 'provider', providerId, fromGroupIdx, fromProviderIdx })
  }

  const handleGroupDragStart = (fromGroupIdx: number) => {
    setDragInfo({ type: 'group', fromGroupIdx })
  }

  const handleDragOverGroup = (e: React.DragEvent, groupIdx: number) => {
    e.preventDefault()
    e.dataTransfer.dropEffect = 'move'
    setDropTargetGroup(groupIdx)
  }

  const handleDragLeave = () => {
    setDropTargetGroup(null)
  }

  const handleDropOnGroup = (targetGroupIdx: number) => {
    setDropTargetGroup(null)
    if (!dragInfo) return

    if (dragInfo.type === 'provider') {
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
    } else if (dragInfo.type === 'group') {
      const { fromGroupIdx } = dragInfo
      if (fromGroupIdx === targetGroupIdx) {
        setDragInfo(null)
        return
      }

      const newGroups = [...groups]
      const [moved] = newGroups.splice(fromGroupIdx, 1)
      newGroups.splice(targetGroupIdx, 0, moved)
      onGroupsChange(newGroups)
    }

    setDragInfo(null)
  }

  const handleDropOnNewGroup = () => {
    if (!dragInfo || dragInfo.type !== 'provider') {
      setDragInfo(null)
      return
    }

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
          <Text type="secondary">Chain not configured. Requests without model-specific mappings will fail.</Text>
        </div>
        {addingProvider ? (
          <Select
            autoFocus
            size="small"
            style={{ minWidth: 200 }}
            placeholder="Select provider..."
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
            Add to chain
          </Button>
        )}
      </div>
    )
  }

  return (
    <div
      style={{ marginTop: 12, flexShrink: 0 }}
      onDragOver={(e) => { e.preventDefault(); e.dataTransfer.dropEffect = 'move' }}
      onDrop={(e) => { e.preventDefault(); handleDropOnNewGroup() }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
        <ThunderboltOutlined style={{ color: '#1677ff' }} />
        <Text strong style={{ fontSize: 13 }}>Failover priority</Text>
        <Text type="secondary" style={{ fontSize: 11 }}>
          Same group = same priority (parallel), arrows = sequential failover. Drag groups or individual providers.
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
              onDragLeave={handleDragLeave}
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
                opacity: dragInfo?.type === 'group' && dragInfo.fromGroupIdx === groupIdx ? 0.4 : 1,
                transition: 'all 0.2s',
              }}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <MenuOutlined
                  draggable
                  onDragStart={(e) => {
                    e.dataTransfer.effectAllowed = 'move'
                    e.stopPropagation()
                    handleGroupDragStart(groupIdx)
                  }}
                  onDragEnd={handleDragEnd}
                  style={{ color: '#bfbfbf', fontSize: 14, cursor: 'grab', flexShrink: 0, padding: 2 }}
                />
                <Tag
                  color="blue"
                  style={{
                    margin: 0,
                    fontSize: 11,
                    fontWeight: 700,
                    padding: '0 6px',
                  }}
                >
                  P{groupIdx + 1}
                </Tag>
              </div>
              {group.map((provider, providerIdx) => (
                <Tooltip key={provider.id} title="Drag to another group to merge, drag to empty space to split">
                  <div
                    draggable
                    onDragStart={(e) => {
                      e.dataTransfer.effectAllowed = 'move'
                      e.stopPropagation()
                      handleProviderDragStart(provider.id, groupIdx, providerIdx)
                    }}
                    onDragEnd={handleDragEnd}
                    style={{
                      padding: '4px 10px',
                      background: dragInfo?.type === 'provider' && (dragInfo as any).providerId === provider.id ? '#e6f4ff' : '#fff',
                      border: `1px solid ${dragInfo?.type === 'provider' && (dragInfo as any).providerId === provider.id ? '#1677ff' : provider.enabled ? '#b7eb8f' : '#d9d9d9'}`,
                      borderRadius: 6,
                      cursor: 'grab',
                      display: 'flex',
                      alignItems: 'center',
                      gap: 6,
                      userSelect: 'none',
                      opacity: dragInfo?.type === 'provider' && (dragInfo as any).providerId === provider.id ? 0.5 : 1,
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
                        off
                      </Tag>
                    )}
                    <Popconfirm
                      title="Remove from chain?"
                      onConfirm={() => handleRemoveProvider(groupIdx, providerIdx)}
                      okText="Remove"
                      cancelText="Cancel"
                    >
                      <span
                        style={{ fontSize: 14, color: '#bfbfbf', cursor: 'pointer', lineHeight: 1 }}
                        onClick={(e) => e.stopPropagation()}
                      >
                        x
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
              placeholder="Add provider..."
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
              Add
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}
