import type { Provider, ModelMapping } from '../../api/types'

export interface PipelineModel {
  modelName: string
  providerIds: number[]
}

export interface PipelineProvider {
  provider: Provider
  modelCount: number
}

export type PipelineNodeType = 'model' | 'provider'

export type ModelNodeData = Record<string, unknown> & {
  modelName: string
  providerCount: number
  providerNames: string[]
  onClick?: (modelName: string) => void
}

export type ProviderNodeData = Record<string, unknown> & {
  provider: Provider
  modelCount: number
  onClick?: (provider: Provider) => void
}

export type PriorityEdgeData = Record<string, unknown> & {
  priority: number
  enabled: boolean
  mapping: ModelMapping
  onClick?: (mapping: ModelMapping) => void
}
