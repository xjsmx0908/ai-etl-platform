import type { LucideIcon } from "lucide-react";

export function EmptyState({
  icon: Icon,
  title,
  description,
  action,
}: {
  icon?: LucideIcon;
  title: string;
  description?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 py-10 text-center">
      {Icon && <Icon className="h-8 w-8 text-slate-300" />}
      <p className="text-sm text-slate-500">{title}</p>
      {description && <p className="text-xs text-slate-400">{description}</p>}
      {action}
    </div>
  );
}
