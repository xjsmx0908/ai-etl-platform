"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { apiClient } from "@/lib/apiClient";
import { Logo } from "@/components/ui/Logo";
import { Input } from "@/components/ui/Input";
import { Button } from "@/components/ui/Button";
import { CheckCircle2, Eye, EyeOff, Lock, ShieldCheck, Sparkles, User } from "lucide-react";
import type { InviteLookup } from "@/lib/types";

const FEATURES = [
  { icon: Sparkles, label: "多路召回 · 只回答已发布知识" },
  { icon: ShieldCheck, label: "权限隔离 · 引用可追溯" },
];

const ROLE_LABELS: Record<string, string> = {
  admin: "管理员",
  user: "普通用户",
  readonly: "只读用户",
};

function formatExpiry(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("zh-CN", { hour12: false });
}

// Byte length, not character count: the ceiling is bcrypt's 72-byte input limit,
// so a password of 30 Chinese characters is already over it even though it is
// well under 72 characters.
function byteLength(value: string): number {
  return new TextEncoder().encode(value).length;
}

type Phase = "loading" | "invalid" | "form" | "done";

export default function AcceptInvitePage() {
  const params = useParams<{ token: string }>();
  const token = params?.token ?? "";

  const [phase, setPhase] = useState<Phase>("loading");
  const [invite, setInvite] = useState<InviteLookup | null>(null);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [showPw, setShowPw] = useState(false);
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [createdUsername, setCreatedUsername] = useState("");

  useEffect(() => {
    if (!token) {
      setPhase("invalid");
      return;
    }
    let cancelled = false;
    apiClient
      .getInvite(token)
      .then((data) => {
        if (cancelled) return;
        setInvite(data);
        setPhase("form");
      })
      .catch(() => {
        if (!cancelled) setPhase("invalid");
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  const minRunes = invite?.min_password_runes ?? 8;
  const maxBytes = invite?.max_password_bytes ?? 72;
  const runes = Array.from(password).length;
  const bytes = byteLength(password);
  const tooShort = password.length > 0 && runes < minRunes;
  const tooLong = bytes > maxBytes;
  const mismatch = confirm.length > 0 && confirm !== password;
  const canSubmit = password.length > 0 && confirm === password && !tooShort && !tooLong && !submitting;

  const onSubmit = useCallback(
    async (e: React.FormEvent) => {
      e.preventDefault();
      if (!canSubmit) return;
      setSubmitting(true);
      setError("");
      try {
        const result = await apiClient.acceptInvite(token, password);
        setCreatedUsername(result.username);
        setPassword("");
        setConfirm("");
        setPhase("done");
      } catch (err) {
        const message = (err as Error).message || "设置密码失败";
        setError(message);
        // A link that died between opening the page and submitting (used in
        // another tab, revoked, or expired) is not a form error: there is
        // nothing left to retry, so the page switches to the terminal state.
        if (message.includes("无效或已过期")) setPhase("invalid");
      } finally {
        setSubmitting(false);
      }
    },
    [canSubmit, password, token]
  );

  return (
    <div className="relative flex min-h-screen bg-slate-50">
      {/* 左：品牌渐变面板 */}
      <div className="bg-brand-gradient relative hidden w-1/2 flex-col justify-between overflow-hidden p-12 text-white lg:flex">
        <div className="pointer-events-none absolute -right-24 -top-24 h-96 w-96 rounded-full bg-white/10 blur-3xl" />
        <div className="pointer-events-none absolute -bottom-32 -left-16 h-80 w-80 rounded-full bg-white/10 blur-3xl" />
        <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_top_right,rgba(255,255,255,0.14),transparent_55%)]" />

        <div className="relative">
          <Logo size="lg" inverse />
        </div>

        <div className="relative">
          <p className="text-xs font-semibold uppercase tracking-widest text-blue-100/80">企业智能知识平台</p>
          <h1 className="mt-3 text-3xl font-semibold leading-tight">
            设置密码即可加入
            <br />
            无需管理员再操作
          </h1>
          <div className="mt-8 space-y-3">
            {FEATURES.map(({ icon: Icon, label }) => (
              <div key={label} className="flex items-center gap-3 text-sm text-blue-100">
                <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-white/15">
                  <Icon className="h-4 w-4" />
                </span>
                {label}
              </div>
            ))}
          </div>
        </div>

        <p className="relative text-xs text-blue-200/70">© 2026 知境 · 企业知识库</p>
      </div>

      {/* 右：接受邀请表单 */}
      <div className="relative flex flex-1 items-center justify-center px-4 py-10">
        <div className="pointer-events-none absolute right-10 top-10 hidden h-40 w-40 rounded-full bg-blue-50 lg:block" />
        <div className="pointer-events-none absolute bottom-10 left-10 hidden h-56 w-56 rounded-full bg-indigo-50 lg:block" />

        <div className="relative w-full max-w-sm">
          <div className="mb-8 flex justify-center lg:hidden">
            <Logo size="lg" />
          </div>
          <div className="rounded-2xl border border-slate-200 bg-white p-8 shadow-xl shadow-slate-200/60">
            {phase === "loading" && (
              <p className="py-6 text-center text-sm text-slate-400">正在校验邀请链接…</p>
            )}

            {phase === "invalid" && (
              <div className="text-center">
                <h1 className="text-xl font-semibold text-slate-900">邀请链接无效或已过期</h1>
                <p className="mt-2 text-sm text-slate-500">
                  链接只能用一次，且会到期。请联系系统管理员重新发送一条邀请。
                </p>
                <Link
                  href="/login"
                  className="mt-6 inline-flex h-11 w-full items-center justify-center rounded-lg bg-blue-600 px-4 text-sm font-medium text-white transition hover:bg-blue-700"
                >
                  返回登录
                </Link>
              </div>
            )}

            {phase === "form" && invite && (
              <>
                <div className="flex flex-col items-center text-center lg:items-start lg:text-left">
                  <h1 className="text-xl font-semibold text-slate-900">设置你的登录密码</h1>
                  <p className="mt-1 text-sm text-slate-500">
                    设置完成后即可登录，全程不需要管理员介入。
                  </p>
                </div>

                <dl className="mt-5 space-y-2 rounded-lg bg-slate-50 px-4 py-3 text-sm">
                  <div className="flex items-center justify-between gap-3">
                    <dt className="text-slate-500">用户名</dt>
                    <dd className="font-medium text-slate-800">{invite.username}</dd>
                  </div>
                  <div className="flex items-center justify-between gap-3">
                    <dt className="text-slate-500">角色</dt>
                    <dd className="text-slate-800">{ROLE_LABELS[invite.role] || invite.role}</dd>
                  </div>
                  <div className="flex items-center justify-between gap-3">
                    <dt className="text-slate-500">链接有效期至</dt>
                    <dd className="text-slate-800">{formatExpiry(invite.expires_at)}</dd>
                  </div>
                </dl>

                <form onSubmit={onSubmit} className="mt-5 space-y-4">
                  <div>
                    <label className="mb-1.5 block text-xs font-medium text-slate-600">密码</label>
                    <div className="relative">
                      <Lock className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-400" />
                      <Input
                        type={showPw ? "text" : "password"}
                        value={password}
                        onChange={(e) => setPassword(e.target.value)}
                        autoComplete="new-password"
                        placeholder={`至少 ${minRunes} 个字符`}
                        className="pl-9 pr-10"
                      />
                      <button
                        type="button"
                        onClick={() => setShowPw((v) => !v)}
                        className="absolute right-2.5 top-1/2 -translate-y-1/2 text-slate-400 hover:text-slate-600"
                        aria-label="切换密码可见"
                      >
                        {showPw ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                      </button>
                    </div>
                    {tooShort && (
                      <p className="mt-1 text-xs text-red-600">密码至少需要 {minRunes} 个字符</p>
                    )}
                    {tooLong && (
                      <p className="mt-1 text-xs text-red-600">密码不能超过 {maxBytes} 个字节（当前 {bytes}）</p>
                    )}
                  </div>
                  <div>
                    <label className="mb-1.5 block text-xs font-medium text-slate-600">确认密码</label>
                    <div className="relative">
                      <User className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-400" />
                      <Input
                        type={showPw ? "text" : "password"}
                        value={confirm}
                        onChange={(e) => setConfirm(e.target.value)}
                        autoComplete="new-password"
                        placeholder="再输入一次"
                        className="pl-9"
                      />
                    </div>
                    {mismatch && <p className="mt-1 text-xs text-red-600">两次输入的密码不一致</p>}
                  </div>
                  {error && (
                    <div className="rounded-lg bg-red-50 px-3 py-2.5 text-sm text-red-700">{error}</div>
                  )}
                  <Button type="submit" className="w-full" size="lg" loading={submitting} disabled={!canSubmit}>
                    {submitting ? "创建中…" : "设置密码并创建账号"}
                  </Button>
                  <p className="text-center text-xs text-slate-400">
                    设置完成后请用这个用户名和新密码登录。
                  </p>
                </form>
              </>
            )}

            {phase === "done" && (
              <div className="text-center">
                <span className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-emerald-50">
                  <CheckCircle2 className="h-6 w-6 text-emerald-600" />
                </span>
                <h1 className="mt-4 text-xl font-semibold text-slate-900">账号已创建</h1>
                <p className="mt-2 text-sm text-slate-500">
                  用户名 <span className="font-medium text-slate-800">{createdUsername}</span>
                  ，请用刚才设置的密码登录。
                </p>
                <Link
                  href="/login"
                  className="mt-6 inline-flex h-11 w-full items-center justify-center rounded-lg bg-blue-600 px-4 text-sm font-medium text-white transition hover:bg-blue-700"
                >
                  去登录
                </Link>
              </div>
            )}
          </div>
          <p className="mt-6 text-center text-xs text-slate-400">
            链接异常或账户需协助，请联系系统管理员
          </p>
        </div>
      </div>
    </div>
  );
}
