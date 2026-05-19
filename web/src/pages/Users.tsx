import { useState, useEffect } from 'react'
import { Table, Button, Modal, Form, Input, Select, Switch, Space, Popconfirm, message, Tag } from 'antd'
import { PlusOutlined, EditOutlined, DeleteOutlined } from '@ant-design/icons'
import { fetchUsers, createUser, updateUser, deleteUser } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import type { User } from '../api/types'

export default function Users() {
  const { role } = useAuth()
  const isAdmin = role === 'admin'
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(false)
  const [modalOpen, setModalOpen] = useState(false)
  const [editingUser, setEditingUser] = useState<User | null>(null)
  const [form] = Form.useForm()

  const loadUsers = async () => {
    setLoading(true)
    try {
      const data = await fetchUsers()
      setUsers(data)
    } catch {
      message.error('加载用户列表失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadUsers()
  }, [])

  const openCreate = () => {
    setEditingUser(null)
    form.resetFields()
    form.setFieldsValue({ role: 'user', enabled: true })
    setModalOpen(true)
  }

  const openEdit = (user: User) => {
    setEditingUser(user)
    form.setFieldsValue({ username: user.username, role: user.role, enabled: user.enabled })
    setModalOpen(true)
  }

  const handleSubmit = async () => {
    try {
      const values = await form.validateFields()
      if (editingUser) {
        await updateUser(editingUser.id, values)
        message.success('用户已更新')
      } else {
        await createUser(values)
        message.success('用户已创建')
      }
      setModalOpen(false)
      loadUsers()
    } catch (err) {
      if (err instanceof Error) message.error(err.message)
    }
  }

  const handleDelete = async (id: number) => {
    try {
      await deleteUser(id)
      message.success('用户已删除')
      loadUsers()
    } catch (err) {
      if (err instanceof Error) message.error(err.message)
    }
  }

  const columns = [
    { title: 'ID', dataIndex: 'id', key: 'id', width: 80 },
    { title: '用户名', dataIndex: 'username', key: 'username' },
    {
      title: '角色', dataIndex: 'role', key: 'role', width: 100,
      render: (r: string) => <Tag color={r === 'admin' ? 'blue' : 'default'}>{r === 'admin' ? '管理员' : '普通用户'}</Tag>,
    },
    {
      title: '状态', dataIndex: 'enabled', key: 'enabled', width: 80,
      render: (v: boolean) => <Tag color={v ? 'green' : 'red'}>{v ? '启用' : '禁用'}</Tag>,
    },
    { title: '创建时间', dataIndex: 'created_at', key: 'created_at', width: 180 },
    {
      title: '操作', key: 'actions', width: 160,
      render: (_: unknown, record: User) => (
        isAdmin && (
          <Space>
            <Button type="link" icon={<EditOutlined />} onClick={() => openEdit(record)}>编辑</Button>
            <Popconfirm title="确定删除此用户？" onConfirm={() => handleDelete(record.id)}>
              <Button type="link" danger icon={<DeleteOutlined />}>删除</Button>
            </Popconfirm>
          </Space>
        )
      ),
    },
  ]

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <h2 style={{ margin: 0 }}>用户管理</h2>
        {isAdmin && (
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建用户</Button>
        )}
      </div>

      <Table dataSource={users} columns={columns} rowKey="id" loading={loading} pagination={false} />

      <Modal
        title={editingUser ? '编辑用户' : '新建用户'}
        open={modalOpen}
        onOk={handleSubmit}
        onCancel={() => setModalOpen(false)}
        destroyOnClose
      >
        <Form form={form} layout="vertical" style={{ marginTop: 16 }}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input />
          </Form.Item>
          <Form.Item
            name="password"
            label={editingUser ? '新密码（留空不修改）' : '密码'}
            rules={editingUser ? [] : [{ required: true, message: '请输入密码' }]}
          >
            <Input.Password />
          </Form.Item>
          <Form.Item name="role" label="角色" rules={[{ required: true }]}>
            <Select>
              <Select.Option value="admin">管理员</Select.Option>
              <Select.Option value="user">普通用户</Select.Option>
            </Select>
          </Form.Item>
          {editingUser && (
            <Form.Item name="enabled" label="启用" valuePropName="checked">
              <Switch />
            </Form.Item>
          )}
        </Form>
      </Modal>
    </div>
  )
}
