import { memo } from 'react'
import { Handle, Position, type NodeProps } from '@xyflow/react'
import { Tooltip, Badge } from 'antd'
import type { ModelNodeData } from './types'

function ModelNode({ data, selected }: NodeProps) {
  const nodeData = data as unknown as ModelNodeData
  const { modelName, providerCount, providerNames, onClick } = nodeData

  return (
    <div
      onClick={() => onClick?.(modelName)}
      style={{
        width: 180,
        padding: '12px 16px',
        background: selected ? '#bae0ff' : '#e6f4ff',
        border: `2px solid ${selected ? '#0958d9' : '#1677ff'}`,
        borderRadius: 12,
        cursor: 'pointer',
        boxShadow: selected ? '0 2px 8px rgba(22,119,255,0.3)' : '0 1px 4px rgba(0,0,0,0.08)',
        transition: 'all 0.2s ease',
        position: 'relative',
      }}
    >
      <Handle
        type="source"
        position={Position.Right}
        style={{
          width: 10,
          height: 10,
          background: '#1677ff',
          border: '2px solid #fff',
          right: -5,
        }}
      />
      <div style={{ fontWeight: 600, fontSize: 14, color: '#1d1d1d', marginBottom: 4, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {modelName}
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <Badge count={providerCount} size="small" color={providerCount > 0 ? '#1677ff' : '#d9d9d9'} overflowCount={99} />
        <span style={{ fontSize: 12, color: '#8c8c8c' }}>
          {providerCount === 0 ? '无供应商' : providerCount === 1 ? '1 个供应商' : `${providerCount} 个供应商`}
        </span>
      </div>
      {providerNames.length > 0 && (
        <Tooltip title={providerNames.join('、')} placement="right">
          <div style={{ marginTop: 6, fontSize: 11, color: '#8c8c8c', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {providerNames.join('、')}
          </div>
        </Tooltip>
      )}
    </div>
  )
}

export default memo(ModelNode)
