"use client";

import { useCallback, useEffect, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import { useAuth } from "@/lib/auth";
import type { User } from "@/lib/types";

const ROLE_LABELS: Record<string, string> = {
  admin: "管理员",
  user: "普通用户",
  readonly: "只读用户",
};

function formatDate(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("zh-CN", { hour12: false });
}

function Modal({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: React.ReactNode;
}) {
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/40 p-4"
      onClick={onClose}
    >
      <div
        className="w-full max-w-md rounded-xl border border-slate-200 bg-white p-6 shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="mb-4 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-slate-800">{title}</h3>
          <button onClick={onClose} className="text-slate-400 hover:text-slate-600">
            ✕
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}

const inputCls =
  "w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-500";

export default function UsersPage() {
  const { isAdmin, user } = useAuth();
  const [mounted, setMounted] = useState(false);
  const [users, setUsers] = useState<User[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => setMounted(true), []);

  const [showCreate, setShowCreate] = useState(false);
  const [newUser, setNewUser] = useState({
    username: "",
    password: "",
    role: "user",
    tenant_id: "",
    active: true,
  });

  const [editing, setEditing] = useState<User | null>(null);
  const [editForm, setEditForm] = useState({ role: "user", tenant_id: "", active: true });

  const [resetting, setResetting] = useState<User | null>(null);
  const [newPassword, setNewPassword] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const data = await apiClient.listUsers({ limit: 100 });
      setUsers(data.items);
      setTotal(data.total);
    } catch (e) {
      setError((e as Error).message || "加载失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (isAdmin) void load();
  }, [isAdmin, load]);

  const onCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");
    try {
      await apiClient.createUser({
        username: newUser.username.trim(),
        password: newUser.password,
        role: newUser.role,
        tenant_id: newUser.tenant_id.trim(),
        active: newUser.active,
      });
      setShowCreate(false);
      setNewUser({ username: "", password: "", role: "user", tenant_id: "", active: true });
      void load();
    } catch (e) {
      setError((e as Error).message || "创建失败");
    }
  };

  const openEdit = (u: User) => {
    setEditing(u);
    setEditForm({ role: u.role, tenant_id: u.tenant_id, active: u.active });
  };

  const onSaveEdit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!editing) return;
    setError("");
    try {
      await apiClient.updateUser(editing.id, {
        role: editForm.role,
        tenant_id: editForm.tenant_id.trim() || undefined,
        active: editForm.active,
      });
      setEditing(null);
      void load();
    } catch (e) {
      setError((e as Error).message || "更新失败");
    }
  };

  const onResetPassword = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!resetting) return;
    setError("");
    try {
      await apiClient.setUserPassword(resetting.id, newPassword);
      setResetting(null);
      setNewPassword("");
    } catch (e) {
      setError((e as Error).message || "重置密码失败");
    }
  };

  const onDelete = async (u: User) => {
    if (!window.confirm(`确认删除用户「${u.username}」？该操作不可恢复。`)) return;
    setError("");
    try {
      await apiClient.deleteUser(u.id);
      void load();
    } catch (e) {
      setError((e as Error).message || "删除失败");
    }
  };

  if (!mounted) {
    return (
      <div className="flex min-h-[60vh] items-center justify-center text-sm text-slate-400">
        加载中…
      </div>
    );
  }

  if (!isAdmin) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-10 text-center shadow-sm">
        <p className="text-sm font-medium text-slate-700">无权访问</p>
        <p className="mt-1 text-xs text-slate-500">该页面仅管理员可见。</p>
      </div>
    );
  }

  if (user && !isAdmin) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-10 text-center shadow-sm">
        <p className="text-sm font-medium text-slate-700">无权访问</p>
        <p className="mt-1 text-xs text-slate-500">该页面仅管理员可见。</p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-slate-500">用户管理</h2>
        <button
          onClick={() => setShowCreate(true)}
          className="rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700"
        >
          新建用户
        </button>
      </div>

      {error && <div className="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700">{error}</div>}

      <div className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm">
        <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3">
          <h3 className="text-sm font-semibold text-slate-500">用户列表</h3>
          <span className="text-xs text-slate-400">共 {total} 个用户</span>
        </div>
        {loading ? (
          <div className="p-8 text-center text-sm text-slate-400">加载中…</div>
        ) : users.length === 0 ? (
          <div className="p-8 text-center text-sm text-slate-400">暂无用户</div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="bg-slate-50 text-xs text-slate-500">
                <tr>
                  <th className="px-4 py-2 font-medium">用户名</th>
                  <th className="px-4 py-2 font-medium">角色</th>
                  <th className="px-4 py-2 font-medium">租户</th>
                  <th className="px-4 py-2 font-medium">状态</th>
                  <th className="px-4 py-2 font-medium">创建时间</th>
                  <th className="px-4 py-2 font-medium">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {users.map((u) => (
                  <tr key={u.id} className="hover:bg-slate-50/60">
                    <td className="px-4 py-2.5 font-medium text-slate-700">{u.username}</td>
                    <td className="px-4 py-2.5">
                      <span className="rounded bg-slate-100 px-2 py-0.5 text-xs text-slate-600">
                        {ROLE_LABELS[u.role] || u.role}
                      </span>
                    </td>
                    <td className="px-4 py-2.5 font-mono text-xs text-slate-600">{u.tenant_id}</td>
                    <td className="px-4 py-2.5">
                      <span
                        className={
                          u.active
                            ? "rounded bg-emerald-50 px-2 py-0.5 text-xs font-medium text-emerald-700"
                            : "rounded bg-red-50 px-2 py-0.5 text-xs font-medium text-red-700"
                        }
                      >
                        {u.active ? "启用" : "停用"}
                      </span>
                    </td>
                    <td className="px-4 py-2.5 text-xs text-slate-500">{formatDate(u.created_at)}</td>
                    <td className="px-4 py-2.5">
                      <div className="flex gap-1">
                        <button
                          onClick={() => openEdit(u)}
                          className="rounded-md px-2 py-1 text-xs text-blue-600 hover:bg-blue-50"
                        >
                          编辑
                        </button>
                        <button
                          onClick={() => setResetting(u)}
                          className="rounded-md px-2 py-1 text-xs text-slate-600 hover:bg-slate-100"
                        >
                          重置密码
                        </button>
                        <button
                          onClick={() => void onDelete(u)}
                          className="rounded-md px-2 py-1 text-xs text-red-600 hover:bg-red-50"
                        >
                          删除
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {showCreate && (
        <Modal title="新建用户" onClose={() => setShowCreate(false)}>
          <form onSubmit={onCreate} className="space-y-3">
            <div>
              <label className="mb-1 block text-xs font-medium text-slate-600">用户名</label>
              <input
                value={newUser.username}
                onChange={(e) => setNewUser((p) => ({ ...p, username: e.target.value }))}
                className={inputCls}
                required
              />
            </div>
            <div>
              <label className="mb-1 block text-xs font-medium text-slate-600">密码</label>
              <input
                type="password"
                value={newUser.password}
                onChange={(e) => setNewUser((p) => ({ ...p, password: e.target.value }))}
                className={inputCls}
                required
              />
            </div>
            <div>
              <label className="mb-1 block text-xs font-medium text-slate-600">角色</label>
              <select
                value={newUser.role}
                onChange={(e) => setNewUser((p) => ({ ...p, role: e.target.value }))}
                className={inputCls}
              >
                <option value="admin">管理员</option>
                <option value="user">普通用户</option>
                <option value="readonly">只读用户</option>
              </select>
            </div>
            <div>
              <label className="mb-1 block text-xs font-medium text-slate-600">租户 ID</label>
              <input
                value={newUser.tenant_id}
                onChange={(e) => setNewUser((p) => ({ ...p, tenant_id: e.target.value }))}
                placeholder="tenant_id"
                className={inputCls}
                required
              />
            </div>
            <label className="flex items-center gap-2 text-sm text-slate-700">
              <input
                type="checkbox"
                checked={newUser.active}
                onChange={(e) => setNewUser((p) => ({ ...p, active: e.target.checked }))}
              />
              启用
            </label>
            <div className="flex justify-end gap-2 pt-2">
              <button
                type="button"
                onClick={() => setShowCreate(false)}
                className="rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm text-slate-600 hover:bg-slate-100"
              >
                取消
              </button>
              <button
                type="submit"
                className="rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700"
              >
                创建
              </button>
            </div>
          </form>
        </Modal>
      )}

      {editing && (
        <Modal title={`编辑用户 · ${editing.username}`} onClose={() => setEditing(null)}>
          <form onSubmit={onSaveEdit} className="space-y-3">
            <div>
              <label className="mb-1 block text-xs font-medium text-slate-600">角色</label>
              <select
                value={editForm.role}
                onChange={(e) => setEditForm((p) => ({ ...p, role: e.target.value }))}
                className={inputCls}
              >
                <option value="admin">管理员</option>
                <option value="user">普通用户</option>
                <option value="readonly">只读用户</option>
              </select>
            </div>
            <div>
              <label className="mb-1 block text-xs font-medium text-slate-600">租户 ID</label>
              <input
                value={editForm.tenant_id}
                onChange={(e) => setEditForm((p) => ({ ...p, tenant_id: e.target.value }))}
                className={inputCls}
              />
            </div>
            <label className="flex items-center gap-2 text-sm text-slate-700">
              <input
                type="checkbox"
                checked={editForm.active}
                onChange={(e) => setEditForm((p) => ({ ...p, active: e.target.checked }))}
              />
              启用
            </label>
            <div className="flex justify-end gap-2 pt-2">
              <button
                type="button"
                onClick={() => setEditing(null)}
                className="rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm text-slate-600 hover:bg-slate-100"
              >
                取消
              </button>
              <button
                type="submit"
                className="rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700"
              >
                保存
              </button>
            </div>
          </form>
        </Modal>
      )}

      {resetting && (
        <Modal title={`重置密码 · ${resetting.username}`} onClose={() => setResetting(null)}>
          <form onSubmit={onResetPassword} className="space-y-3">
            <div>
              <label className="mb-1 block text-xs font-medium text-slate-600">新密码</label>
              <input
                type="password"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                className={inputCls}
                required
                minLength={6}
              />
            </div>
            <div className="flex justify-end gap-2 pt-2">
              <button
                type="button"
                onClick={() => setResetting(null)}
                className="rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm text-slate-600 hover:bg-slate-100"
              >
                取消
              </button>
              <button
                type="submit"
                className="rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700"
              >
                重置
              </button>
            </div>
          </form>
        </Modal>
      )}
    </div>
  );
}
