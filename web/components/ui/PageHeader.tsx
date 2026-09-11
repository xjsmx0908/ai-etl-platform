import type { LucideIcon } from "lucide-react";

export function PageHeader({
  icon: Icon,
  title,
  description,
  actions,
}: {
  icon?: LucideIcon;
  title: string;
  description?: string;
  actions?: React.ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-4">
      <div className="flex items-center gap-3">
        {Icon ? (
          <div className="bg-brand-gradient flex h-10 w-10 items-center justify-center rounded-xl text-white shadow-sm">
            <Icon className="h-5 w-5" />
          </div>
        ) : null}
        <div>
          <h1 className="text-xl font-semibold text-slate-900">{title}</h1>
          {description ? <p className="mt-0.5 text-sm text-slate-500">{description}</p> : null}
        </div>
      </div>
      {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
    </div>
  );
}
