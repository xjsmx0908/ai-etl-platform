"use client";

import { useCallback, useEffect, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import { useAuth } from "@/lib/auth";
import { PageHeader } from "@/components/ui/PageHeader";
import { Button } from "@/components/ui/Button";
import { Check, Copy, Mail, UserPlus, Users } from "lucide-react";
import type { CreatedInvite, Invite, User } from "@/lib/types";

const ROLE_LABELS: Record<string, string> = {
  admin: "管理员",
  user: "普通用户",
  readonly: "只读用户",
};

const INVITE_STATE_LABELS: Record<string, string> = {
  pending: "待接受",
  consumed: "已接受",
  revoked: "已撤销",
  expired: "已过期",
};

const INVITE_STATE_CLASSES: Record<string, string> = {
  pending: "bg-amber-50 text-amber-700",
  consumed: "bg-emerald-50 text-emerald-700",
  revoked: "bg-red-50 text-red-700",
  expired: "bg-slate-100 text-slate-500",
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

  const [invites, setInvites] = useState<Invite[]>([]);
  const [inviteError, setInviteError] = useState("");
  const [showInvite, setShowInvite] = useState(false);
  const [inviteForm, setInviteForm] = useState({ username: "", display_name: "", email: "", role: "user" });
  const [createdInvite, setCreatedInvite] = useState<CreatedInvite | null>(null);
  const [copied, setCopied] = useState(false);

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

  const loadInvites = useCallback(async () => {
    setInviteError("");
    try {
      const data = await apiClient.listInvites({ limit: 50 });
      setInvites(data.items);
    } catch (e) {
      setInviteError((e as Error).message || "邀请列表加载失败");
    }
  }, []);

  useEffect(() => {
    if (isAdmin) {
      void load();
      void loadInvites();
    }
  }, [isAdmin, load, loadInvites]);

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

  const closeInvite = () => {
    setShowInvite(false);
    setCreatedInvite(null);
    setCopied(false);
    setInviteForm({ username: "", display_name: "", email: "", role: "user" });
  };

  const onCreateInvite = async (e: React.FormEvent) => {
    e.preventDefault();
    setInviteError("");
    try {
      const created = await apiClient.createInvite({
        username: inviteForm.username.trim(),
        display_name: inviteForm.display_name.trim(),
        email: inviteForm.email.trim(),
        role: inviteForm.role as User["role"],
      });
      // Keep the modal open and switch it to the link: this response is the
      // only place the token exists, so closing it would lose the link.
      setCreatedInvite(created);
      void loadInvites();
    } catch (err) {
      setInviteError((err as Error).message || "创建邀请失败");
    }
  };

  // The API returns a path relative to the web app's origin, because it cannot
  // know which hostname the browser reached it through. The absolute URL is
  // composed here, where the origin is known.
  const inviteLink = createdInvite ? new URL(createdInvite.accept_path, window.location.origin).toString() : "";

  const onCopyInvite = async () => {
    if (!inviteLink) return;
    try {
      await navigator.clipboard.writeText(inviteLink);
      setCopied(true);
    } catch {
      setInviteError("复制失败，请手动选中链接复制");
    }
  };

  const onRevokeInvite = async (invite: Invite) => {
    if (!window.confirm(`确认撤销「${invite.username}」的邀请链接？撤销后该链接立即失效。`)) return;
    setInviteError("");
    try {
      await apiClient.revokeInvite(invite.id);
      void loadInvites();
    } catch (e) {
      setInviteError((e as Error).message || "撤销失败");
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
      <PageHeader
        icon={Users}
        title="用户管理"
        description="创建、启停账号并重置密码"
        actions={
          <div className="flex gap-2">
            <Button variant="secondary" icon={UserPlus} onClick={() => setShowInvite(true)}>
              邀请用户
            </Button>
            <Button onClick={() => setShowCreate(true)}>新建用户</Button>
          </div>
        }
      />

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

      {inviteError && (
        <div className="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700">{inviteError}</div>
      )}

      <div className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm">
        <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3">
          <h3 className="text-sm font-semibold text-slate-500">邀请记录</h3>
          <span className="text-xs text-slate-400">链接仅生成时可见一次</span>
        </div>
        {invites.length === 0 ? (
          <div className="p-8 text-center text-sm text-slate-400">暂无邀请记录</div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="bg-slate-50 text-xs text-slate-500">
                <tr>
                  <th className="px-4 py-2 font-medium">用户名</th>
                  <th className="px-4 py-2 font-medium">角色</th>
                  <th className="px-4 py-2 font-medium">状态</th>
                  <th className="px-4 py-2 font-medium">有效期至</th>
                  <th className="px-4 py-2 font-medium">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {invites.map((invite) => (
                  <tr key={invite.id} className="hover:bg-slate-50/60">
                    <td className="px-4 py-2.5 font-medium text-slate-700">{invite.username}</td>
                    <td className="px-4 py-2.5">
                      <span className="rounded bg-slate-100 px-2 py-0.5 text-xs text-slate-600">
                        {ROLE_LABELS[invite.role] || invite.role}
                      </span>
                    </td>
                    <td className="px-4 py-2.5">
                      <span
                        className={`rounded px-2 py-0.5 text-xs font-medium ${
                          INVITE_STATE_CLASSES[invite.state] || "bg-slate-100 text-slate-500"
                        }`}
                      >
                        {INVITE_STATE_LABELS[invite.state] || invite.state}
                      </span>
                    </td>
                    <td className="px-4 py-2.5 text-xs text-slate-500">{formatDate(invite.expires_at)}</td>
                    <td className="px-4 py-2.5">
                      {invite.state === "pending" ? (
                        <button
                          onClick={() => void onRevokeInvite(invite)}
                          className="rounded-md px-2 py-1 text-xs text-red-600 hover:bg-red-50"
                        >
                          撤销
                        </button>
                      ) : (
                        <span className="text-xs text-slate-300">—</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {showInvite && (
        <Modal title={createdInvite ? "邀请已生成" : "邀请用户"} onClose={closeInvite}>
          {createdInvite ? (
            <div className="space-y-3">
              <p className="text-sm text-slate-600">
                把下面的链接发给 <span className="font-medium text-slate-800">{createdInvite.invite.username}</span>
                ，对方设置密码后即可登录。链接只能用一次，且会到期。
              </p>
              <div className="break-all rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-xs text-slate-700">
                {inviteLink}
              </div>
              <p className="text-xs text-slate-500">
                有效期至 {formatDate(createdInvite.expires_at)}。关闭后无法再次查看，如需重发请再邀请一次（旧链接会立即失效）。
              </p>
              <div className="flex justify-end gap-2 pt-1">
                <button
                  type="button"
                  onClick={() => void onCopyInvite()}
                  className="inline-flex items-center gap-1.5 rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700"
                >
                  {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
                  {copied ? "已复制" : "复制链接"}
                </button>
                <button
                  type="button"
                  onClick={closeInvite}
                  className="rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm text-slate-600 hover:bg-slate-100"
                >
                  完成
                </button>
              </div>
            </div>
          ) : (
            <form onSubmit={onCreateInvite} className="space-y-3">
              <p className="flex items-start gap-2 rounded-lg bg-blue-50 px-3 py-2 text-xs text-blue-700">
                <Mail className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                系统不发送邮件，生成后请把链接直接发给对方。
              </p>
              <div>
                <label className="mb-1 block text-xs font-medium text-slate-600">用户名</label>
                <input
                  value={inviteForm.username}
                  onChange={(e) => setInviteForm((p) => ({ ...p, username: e.target.value }))}
                  className={inputCls}
                  required
                />
              </div>
              <div>
                <label className="mb-1 block text-xs font-medium text-slate-600">显示名（选填）</label>
                <input
                  value={inviteForm.display_name}
                  onChange={(e) => setInviteForm((p) => ({ ...p, display_name: e.target.value }))}
                  className={inputCls}
                />
              </div>
              <div>
                <label className="mb-1 block text-xs font-medium text-slate-600">邮箱（选填）</label>
                <input
                  type="email"
                  value={inviteForm.email}
                  onChange={(e) => setInviteForm((p) => ({ ...p, email: e.target.value }))}
                  className={inputCls}
                />
              </div>
              <div>
                <label className="mb-1 block text-xs font-medium text-slate-600">角色</label>
                <select
                  value={inviteForm.role}
                  onChange={(e) => setInviteForm((p) => ({ ...p, role: e.target.value }))}
                  className={inputCls}
                >
                  <option value="admin">管理员</option>
                  <option value="user">普通用户</option>
                  <option value="readonly">只读用户</option>
                </select>
              </div>
              <p className="text-xs text-slate-500">
                邀请归属于你所在的租户，对方无需管理员再介入即可完成开户。
              </p>
              <div className="flex justify-end gap-2 pt-2">
                <button
                  type="button"
                  onClick={closeInvite}
                  className="rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm text-slate-600 hover:bg-slate-100"
                >
                  取消
                </button>
                <button
                  type="submit"
                  className="rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700"
                >
                  生成邀请链接
                </button>
              </div>
            </form>
          )}
        </Modal>
      )}

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
