import { BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer } from 'recharts'
import type { AccessLogStatsResponse } from '../api/types'
import { ChartErrorBoundary } from './ChartErrorBoundary'

interface Props {
  data: AccessLogStatsResponse | null
  mode: 'access' | 'token'
}

function getChartData(stats: AccessLogStatsResponse | null) {
  if (!stats || !stats.buckets || stats.buckets.length === 0) return []
  return stats.buckets.map(b => ({
    time: new Date(b.start).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }),
    '有故障转移': b.count_with_failover,
    '无故障转移': b.count_without_failover,
    '有故障转移Token': b.tokens_in_with_failover + b.tokens_out_with_failover,
    '无故障转移Token': b.tokens_in_without_failover + b.tokens_out_without_failover,
  }))
}

export default function AccessLogChart({ data, mode }: Props) {
  const chartData = getChartData(data)

  if (chartData.length === 0) {
    return <p style={{ textAlign: 'center', color: '#999', padding: 40 }}>暂无统计数据</p>
  }

  const isToken = mode === 'token'

  return (
    <ChartErrorBoundary>
      <ResponsiveContainer width="100%" height={450}>
        <BarChart data={chartData} margin={{ top: 5, right: 30, left: 20, bottom: 5 }} barCategoryGap="30%">
          <CartesianGrid strokeDasharray="3 3" />
          <XAxis dataKey="time" />
          <YAxis />
          <Tooltip />
          <Legend />
          <Bar dataKey={isToken ? '有故障转移Token' : '有故障转移'} stackId="stack" fill={isToken ? '#faad14' : '#ff4d4f'} maxBarSize={60} />
          <Bar dataKey={isToken ? '无故障转移Token' : '无故障转移'} stackId="stack" fill={isToken ? '#52c41a' : '#1677ff'} maxBarSize={60} />
        </BarChart>
      </ResponsiveContainer>
    </ChartErrorBoundary>
  )
}
