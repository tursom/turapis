import { Component, type ReactNode } from 'react'

interface Props {
  children: ReactNode
  fallback?: ReactNode
}

interface State {
  hasError: boolean
  error: string
}

export class ChartErrorBoundary extends Component<Props, State> {
  state: State = { hasError: false, error: '' }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error: error.message }
  }

  render() {
    if (this.state.hasError) {
      return this.props.fallback ?? (
        <div style={{ padding: 40, textAlign: 'center', color: '#ff4d4f', background: '#fff2f0', borderRadius: 6, border: '1px solid #ffccc7' }}>
          <p style={{ fontWeight: 600, marginBottom: 8 }}>图表加载失败</p>
          <p style={{ fontSize: 13, color: '#666' }}>{this.state.error}</p>
        </div>
      )
    }
    return this.props.children
  }
}
