import { useMemo, useCallback, useEffect, useState } from 'react'
import {
  ReactFlow,
  Controls,
  MiniMap,
  Background,
  BackgroundVariant,
  useNodesState,
  useEdgesState,
  MarkerType,
  type Connection,
  type Node,
  type Edge,
  type OnConnect,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { Spin, Empty, Button, Space, Typography } from 'antd'
import { ClearOutlined, SettingOutlined } from '@ant-design/icons'
import type { Provider, ModelMapping } from '../../api/types'
import type { ModelNodeData, ProviderNodeData, PriorityEdgeData } from './types'
import ModelNode from './ModelNode'
import ProviderNode from './ProviderNode'
import PriorityEdge from './PriorityEdge'

const { Text } = Typography

const nodeTypes = { model: ModelNode, provider: ProviderNode }
const edgeTypes = { priority: PriorityEdge }

const MODEL_X = 200
const PROVIDER_X = 650
const NODE_SPACING_Y = 120

interface PipelineCanvasProps {
  providers: Provider[]
  mappings: ModelMapping[]
  loading: boolean
  filterModel?: string
  focusedModel?: string | null
  onFocusModel?: (modelName: string | null) => void
  onCreateMapping: (modelName: string, providerId: number) => void
  onEditMapping: (mapping: ModelMapping) => void
  onDeleteMapping: (id: number) => void
  onClearModelMappings?: (modelName: string) => void
  onClearProviderMappings?: (providerId: number) => void
}

function computeNodes(
  providers: Provider[],
  mappings: ModelMapping[],
  filterModel: string,
  focusedModel: string | null | undefined,
): Node[] {
  const nodes: Node[] = []
  const filter = filterModel.trim().toLowerCase()

  let modelNames = [...new Set(mappings.map((m) => m.model_name))].sort()
  if (focusedModel) {
    modelNames = modelNames.filter((n) => n === focusedModel)
  }

  const visibleModels = new Set<string>()

  modelNames.forEach((modelName, i) => {
    const matchesFilter = !filter || modelName.toLowerCase().includes(filter)
    if (matchesFilter) visibleModels.add(modelName)

    const modelMappings = mappings.filter((m) => m.model_name === modelName && m.enabled)
    const linkedProviderIds = [...new Set(modelMappings.map((m) => m.provider_id))]
    const linkedProviderNames = linkedProviderIds
      .map((pid) => providers.find((p) => p.id === pid)?.name)
      .filter((n): n is string => n !== undefined)

    const data: ModelNodeData = {
      modelName,
      providerCount: linkedProviderIds.length,
      providerNames: linkedProviderNames,
    }

    nodes.push({
      id: `model:${modelName}`,
      type: 'model',
      position: { x: MODEL_X, y: 60 + i * NODE_SPACING_Y },
      data,
      draggable: true,
      hidden: !matchesFilter,
    })
  })

  providers.forEach((provider, i) => {
    const connectedModels = [...new Set(
      mappings
        .filter((m) => m.provider_id === provider.id && m.enabled)
        .map((m) => m.model_name),
    )]
    const visibleConnected = focusedModel
      ? connectedModels.filter((m) => m === focusedModel)
      : filter
        ? connectedModels.filter((m) => visibleModels.has(m))
        : connectedModels
    const modelCount = visibleConnected.length

    const data: ProviderNodeData = {
      provider,
      modelCount,
    }

    nodes.push({
      id: `provider:${provider.id}`,
      type: 'provider',
      position: { x: PROVIDER_X, y: 60 + i * NODE_SPACING_Y },
      data,
      draggable: true,
      hidden: focusedModel
        ? modelCount === 0
        : filter
          ? modelCount === 0
          : false,
    })
  })

  return nodes
}

function computeEdges(mappings: ModelMapping[], onEditMapping: (mapping: ModelMapping) => void): Edge[] {
  const edges: Edge[] = []
  const enabledMappings = mappings
    .filter((m) => m.enabled)
    .sort((a, b) => a.priority - b.priority)

  const modelPriorityCounter = new Map<string, number>()
  enabledMappings.forEach((m) => {
    const count = modelPriorityCounter.get(m.model_name) ?? 0
    modelPriorityCounter.set(m.model_name, count + 1)
    const priorityLabel = count + 1

    const data: PriorityEdgeData = {
      priority: priorityLabel,
      enabled: m.enabled,
      mapping: m,
      onClick: onEditMapping,
    }

    edges.push({
      id: `edge:${m.id}`,
      type: 'priority',
      source: `model:${m.model_name}`,
      target: `provider:${m.provider_id}`,
      sourceHandle: null,
      targetHandle: null,
      markerEnd: { type: MarkerType.ArrowClosed, color: m.enabled ? '#91caff' : '#d9d9d9', width: 16, height: 16 },
      data,
    })
  })

  return edges
}

export default function PipelineCanvas({
  providers,
  mappings,
  loading,
  filterModel = '',
  focusedModel,
  onFocusModel,
  onCreateMapping,
  onEditMapping,
  onDeleteMapping,
  onClearModelMappings,
  onClearProviderMappings,
}: PipelineCanvasProps) {
  const [contextMenu, setContextMenu] = useState<{
    type: 'model'; modelName: string } | { type: 'provider'; providerId: number } | null>(null)
  const [contextMenuPos, setContextMenuPos] = useState({ x: 0, y: 0 })

  const initialNodes = useMemo(
    () => computeNodes(providers, mappings, filterModel, focusedModel),
    [providers, mappings, filterModel, focusedModel],
  )
  const initialEdges = useMemo(() => computeEdges(mappings, onEditMapping), [mappings, onEditMapping])

  const [nodes, setNodes, onNodesChange] = useNodesState(initialNodes)
  const [edges, setEdges, onEdgesChange] = useEdgesState(initialEdges)

  useEffect(() => {
    setNodes(computeNodes(providers, mappings, filterModel, focusedModel))
  }, [providers, mappings, filterModel, focusedModel, setNodes])

  useEffect(() => {
    setEdges(computeEdges(mappings, onEditMapping))
  }, [mappings, onEditMapping, setEdges])

  const onConnect: OnConnect = useCallback(
    (connection: Connection) => {
      if (!connection.source || !connection.target) return

      const sourceId = connection.source
      const targetId = connection.target

      if (sourceId.startsWith('model:') && targetId.startsWith('provider:')) {
        const modelName = sourceId.replace('model:', '')
        const providerId = parseInt(targetId.replace('provider:', ''), 10)
        if (!isNaN(providerId)) {
          onCreateMapping(modelName, providerId)
        }
      }
    },
    [onCreateMapping],
  )

  const onEdgeClick = useCallback(
    (_event: React.MouseEvent, edge: Edge) => {
      const edgeData = edge.data as PriorityEdgeData | undefined
      if (edgeData?.mapping) {
        onEditMapping(edgeData.mapping)
      }
    },
    [onEditMapping],
  )

  const handleClearAll = useCallback(() => {
    for (const edge of edges) {
      const edgeId = edge.id.replace('edge:', '')
      const numId = parseInt(edgeId, 10)
      if (!isNaN(numId)) {
        onDeleteMapping(numId)
      }
    }
  }, [edges, onDeleteMapping])

  if (loading) {
    return (
      <div style={{ flex: 1, minHeight: 300, display: 'flex', justifyContent: 'center', alignItems: 'center', background: '#fafafa', borderRadius: 8 }}>
        <Spin size="large" />
        <Text type="secondary" style={{ marginLeft: 12 }}>加载管道数据...</Text>
      </div>
    )
  }

  if (providers.length === 0) {
    return (
      <div style={{ flex: 1, minHeight: 300, display: 'flex', justifyContent: 'center', alignItems: 'center', background: '#fafafa', borderRadius: 8 }}>
        <Empty description="暂无供应商数据，请先添加供应商" />
      </div>
    )
  }

  return (
    <div style={{ position: 'relative', flex: 1, minHeight: 500, background: '#fafafa', borderRadius: 8, overflow: 'hidden' }}>
      {mappings.length === 0 && nodes.length > 0 && (
        <div style={{
          position: 'absolute',
          top: '50%',
          left: '50%',
          transform: 'translate(-50%, -50%)',
          zIndex: 10,
          textAlign: 'center',
          background: 'rgba(255,255,255,0.92)',
          padding: '24px 32px',
          borderRadius: 8,
          pointerEvents: 'none',
        }}>
          <Text type="secondary" style={{ fontSize: 14, display: 'block', marginBottom: 8 }}>
            从左侧模型节点拖拽连接到右侧供应商节点来创建映射
          </Text>
          <Text type="secondary" style={{ fontSize: 12 }}>
            或点击下方"添加映射"按钮
          </Text>
        </div>
      )}
      <div style={{ position: 'absolute', top: 8, right: 8, zIndex: 10 }}>
        <Space>
          {mappings.length > 0 && (
            <Button size="small" icon={<ClearOutlined />} onClick={handleClearAll} danger>
              清除全部
            </Button>
          )}
        </Space>
      </div>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onConnect={onConnect}
        onEdgeClick={onEdgeClick}
        onNodeContextMenu={(event, node) => {
          event.preventDefault()
          if (node.type === 'model') {
            const d = node.data as ModelNodeData
            setContextMenu({ type: 'model', modelName: d.modelName })
            setContextMenuPos({ x: event.clientX, y: event.clientY })
          } else if (node.type === 'provider') {
            const d = node.data as ProviderNodeData
            setContextMenu({ type: 'provider', providerId: d.provider.id })
            setContextMenuPos({ x: event.clientX, y: event.clientY })
          }
        }}
        onPaneClick={() => setContextMenu(null)}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        fitView
        fitViewOptions={{ padding: 0.3 }}
        defaultEdgeOptions={{
          type: 'priority',
          animated: false,
          style: { strokeDasharray: '6 3' },
        }}
        connectionLineStyle={{ stroke: '#1677ff', strokeWidth: 2, strokeDasharray: '4 2' }}
        panOnScroll
        panOnScrollSpeed={1.5}
        zoomOnPinch
        zoomOnDoubleClick={false}
        style={{ width: '100%', height: '100%' }}
        proOptions={{ hideAttribution: true }}
        deleteKeyCode={null}
      >
        <Controls position="bottom-left" />
        <MiniMap
          position="bottom-right"
          nodeStrokeWidth={2}
          nodeColor={(n) => {
            if (n.type === 'model') return '#1677ff'
            if (n.type === 'provider') {
              const d = n.data as ProviderNodeData | undefined
              return d?.provider?.enabled ? '#52c41a' : '#d9d9d9'
            }
            return '#8c8c8c'
          }}
        />
        <Background variant={BackgroundVariant.Dots} gap={20} size={1} color="#e0e0e0" />
        <svg>
          <defs>
            <marker id="arrow" viewBox="0 0 10 10" refX={9} refY={5} markerWidth={6} markerHeight={6} orient="auto-start-reverse">
              <path d="M 0 0 L 10 5 L 0 10 z" fill="#91caff" />
            </marker>
            <marker id="arrow-selected" viewBox="0 0 10 10" refX={9} refY={5} markerWidth={6} markerHeight={6} orient="auto-start-reverse">
              <path d="M 0 0 L 10 5 L 0 10 z" fill="#1677ff" />
            </marker>
          </defs>
        </svg>
      </ReactFlow>
      {contextMenu && (
        <div
          style={{
            position: 'fixed',
            left: contextMenuPos.x,
            top: contextMenuPos.y,
            zIndex: 1000,
            background: '#fff',
            border: '1px solid #d9d9d9',
            borderRadius: 8,
            boxShadow: '0 4px 12px rgba(0,0,0,0.12)',
            padding: 4,
            minWidth: 160,
          }}
          onClick={() => setContextMenu(null)}
        >
          {contextMenu.type === 'model' && (
            <div
              style={{
                padding: '8px 12px',
                cursor: 'pointer',
                borderRadius: 6,
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                fontSize: 13,
              }}
              onClick={(e) => {
                e.stopPropagation()
                setContextMenu(null)
                onFocusModel?.(contextMenu.modelName)
              }}
              onMouseEnter={(e) => { (e.currentTarget as HTMLElement).style.background = '#f0f5ff' }}
              onMouseLeave={(e) => { (e.currentTarget as HTMLElement).style.background = 'transparent' }}
            >
              <SettingOutlined style={{ color: '#1677ff' }} />
              单独配置
            </div>
          )}
          <div
            style={{
              padding: '8px 12px',
              cursor: 'pointer',
              borderRadius: 6,
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              fontSize: 13,
            }}
            onClick={(e) => {
              e.stopPropagation()
              setContextMenu(null)
              if (contextMenu.type === 'model') {
                onClearModelMappings?.(contextMenu.modelName)
              } else {
                onClearProviderMappings?.(contextMenu.providerId)
              }
            }}
            onMouseEnter={(e) => { (e.currentTarget as HTMLElement).style.background = '#fff1f0' }}
            onMouseLeave={(e) => { (e.currentTarget as HTMLElement).style.background = 'transparent' }}
          >
            <ClearOutlined style={{ color: '#ff4d4f' }} />
            清空路由
          </div>
        </div>
      )}
    </div>
  )
}
