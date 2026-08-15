export default function StatCard({
  label,
  value,
  accent,
}: {
  label: string;
  value: string;
  accent?: boolean;
}) {
  return (
    <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
      <p className="text-xs text-slate-500">{label}</p>
      <p
        className={
          accent ? "mt-1 text-xl font-semibold text-emerald-600" : "mt-1 text-xl font-semibold text-slate-800"
        }
      >
        {value}
      </p>
    </div>
  );
}
