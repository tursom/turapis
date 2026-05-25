import { memo } from 'react'
import { Handle, Position, type NodeProps } from '@xyflow/react'
import { Tag, Tooltip } from 'antd'
import { CheckCircleOutlined, CloseCircleOutlined } from '@ant-design/icons'
import type { ProviderNodeData } from './types'
import type { QuotaEntry } from '../../api/types'

function QuotaMiniBar({ entries }: { entries: QuotaEntry[] }) {
  if (entries.length === 0) return null
  return (
    <div style={{ display: 'flex', gap: 3, marginTop: 6, alignItems: 'center' }}>
      {entries.map((e, i) => {
        const pct = e.used_percent
        const color = pct >= 80 ? '#ff4d4f' : pct >= 50 ? '#fa8c16' : '#52c41a'
        const label = e.window_minutes <= 400 ? '5h' : e.window_minutes <= 15000 ? '7d' : '30d'
        return (
          <Tooltip key={i} title={`${label}: ${pct}%`}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
              <span style={{ fontSize: 9, color: '#8c8c8c', width: 18 }}>{label}</span>
              <div style={{ width: 36, height: 6, background: '#f0f0f0', borderRadius: 2, overflow: 'hidden' }}>
                <div style={{ width: `${Math.min(pct, 100)}%`, height: '100%', background: color, borderRadius: 2, transition: 'width .3s' }} />
              </div>
            </div>
          </Tooltip>
        )
      })}
    </div>
  )
}

function ProviderNode({ data, selected }: NodeProps) {
  const nodeData = data as unknown as ProviderNodeData
  const { provider, modelCount, onClick } = nodeData
  const enabled = provider.enabled

  const bgColor = selected ? (enabled ? '#d9f7be' : '#f5f5f5') : enabled ? '#f6ffed' : '#fafafa'
  const borderColor = selected ? (enabled ? '#389e0d' : '#595959') : enabled ? '#52c41a' : '#d9d9d9'

  const quotaEntries: QuotaEntry[] = []
  if (provider.quota?.primary) quotaEntries.push(provider.quota.primary)
  if (provider.quota?.secondary) quotaEntries.push(provider.quota.secondary)
  if (provider.quota?.tertiary) quotaEntries.push(provider.quota.tertiary)

  const protocolLabel = provider.auth_mode === 'oauth'
    ? 'OAuth'
    : provider.protocol === 'openai' ? 'OpenAI' : 'Anthropic'
  const protocolColor = provider.auth_mode === 'oauth' ? 'cyan' : provider.protocol === 'openai' ? 'blue' : 'purple'
  const authLabel = provider.auth_mode === 'oauth' ? 'OAuth' : 'API Key'
  const authColor = provider.auth_mode === 'oauth' ? 'orange' : 'default'

  return (
    <div
      onClick={() => onClick?.(provider)}
      style={{
        width: 180,
        padding: '12px 16px',
        background: bgColor,
        border: `2px solid ${borderColor}`,
        borderRadius: 12,
        cursor: 'pointer',
        boxShadow: selected ? '0 2px 8px rgba(82,196,26,0.3)' : '0 1px 4px rgba(0,0,0,0.08)',
        transition: 'all 0.2s ease',
        position: 'relative',
      }}
    >
      <Handle
        type="target"
        position={Position.Left}
        style={{
          width: 10,
          height: 10,
          background: enabled ? '#52c41a' : '#d9d9d9',
          border: '2px solid #fff',
          left: -5,
        }}
      />
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 4 }}>
        <div style={{ fontWeight: 600, fontSize: 14, color: '#1d1d1d', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1, marginRight: 4 }}>
          <span style={{ color: '#999', fontSize: 10, marginRight: 4 }}>#{provider.id}</span>
          {provider.name}
        </div>
        {enabled
          ? <CheckCircleOutlined style={{ color: '#52c41a', fontSize: 14, flexShrink: 0 }} />
          : <CloseCircleOutlined style={{ color: '#d9d9d9', fontSize: 14, flexShrink: 0 }} />}
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 4, flexWrap: 'wrap', marginBottom: 2 }}>
        <Tag color={protocolColor} style={{ margin: 0, fontSize: 11, lineHeight: '18px', padding: '0 6px' }}>
          {protocolLabel}
        </Tag>
        <Tag color={authColor} style={{ margin: 0, fontSize: 10, lineHeight: '16px', padding: '0 4px' }}>
          {authLabel}
        </Tag>
        <span style={{ fontSize: 11, color: '#8c8c8c' }}>
          {modelCount} 模型
        </span>
      </div>
      <QuotaMiniBar entries={quotaEntries} />
    </div>
  )
}

export default memo(ProviderNode)
