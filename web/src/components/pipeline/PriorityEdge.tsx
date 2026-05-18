import { memo } from 'react'
import { BaseEdge, getBezierPath, EdgeLabelRenderer, type EdgeProps } from '@xyflow/react'
import { Tag } from 'antd'
import type { PriorityEdgeData } from './types'

function PriorityEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  selected,
  data,
}: EdgeProps) {
  const [edgePath, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })

  const edgeData = data as PriorityEdgeData | undefined
  const priority = edgeData?.priority ?? 0
  const enabled = edgeData?.enabled ?? true
  const mapping = edgeData?.mapping

  return (
    <>
      <BaseEdge
        id={id}
        path={edgePath}
        style={{
          stroke: selected ? '#1677ff' : enabled ? '#91caff' : '#d9d9d9',
          strokeWidth: selected ? 2.5 : 1.5,
          strokeDasharray: '6 3',
          animation: 'none',
        }}
        markerEnd={selected ? 'url(#arrow-selected)' : 'url(#arrow)'}
      />
      <EdgeLabelRenderer>
        <div
          style={{
            position: 'absolute',
            transform: `translate(-50%, -50%) translate(${labelX}px,${labelY}px)`,
            pointerEvents: 'all',
            zIndex: 1000,
          }}
        >
          <Tag
            color={enabled ? (selected ? 'blue' : 'processing') : 'default'}
            style={{
              margin: 0,
              cursor: 'pointer',
              fontSize: 11,
              padding: '0 8px',
              borderRadius: 4,
              userSelect: 'none',
            }}
            onClick={(e) => {
              e.stopPropagation()
              if (mapping && edgeData?.onClick) {
                edgeData.onClick(mapping)
              }
            }}
          >
            P{priority}
          </Tag>
        </div>
      </EdgeLabelRenderer>
    </>
  )
}

export default memo(PriorityEdge)
