import type { LucideIcon } from "lucide-react";

export default function StatCard({
  label,
  value,
  tone = "default",
  icon: Icon,
}: {
  label: string;
  value: string;
  tone?: "default" | "success" | "danger";
  icon?: LucideIcon;
}) {
  const valueColor =
    tone === "danger" ? "text-red-600" : tone === "success" ? "text-emerald-600" : "text-slate-800";
  return (
    <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
      <div className="flex items-center justify-between">
        <p className="text-xs text-slate-500">{label}</p>
        {Icon && <Icon className="h-4 w-4 text-slate-400" />}
      </div>
      <p className={`mt-1 text-2xl font-semibold ${valueColor}`}>{value}</p>
    </div>
  );
}
