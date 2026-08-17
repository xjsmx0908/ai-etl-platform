import { ShieldX } from "lucide-react";

export function Forbidden({ message = "该页面仅管理员可见。" }: { message?: string }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-xl border border-slate-200 bg-white p-10 text-center shadow-sm">
      <ShieldX className="h-8 w-8 text-slate-300" />
      <p className="text-sm font-medium text-slate-700">无权访问</p>
      <p className="text-xs text-slate-500">{message}</p>
    </div>
  );
}
