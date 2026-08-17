export type Column<T> = {
  key: string;
  header: React.ReactNode;
  render?: (row: T) => React.ReactNode;
  className?: string;
};

export function DataTable<T>({
  columns,
  rows,
  rowKey,
  loading,
  empty,
}: {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string;
  loading?: boolean;
  empty?: React.ReactNode;
}) {
  if (loading) return <div className="p-8 text-center text-sm text-slate-400">加载中…</div>;
  if (rows.length === 0)
    return <div className="p-8 text-center text-sm text-slate-400">{empty ?? "暂无数据"}</div>;
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-sm">
        <thead className="bg-slate-50 text-xs text-slate-500">
          <tr>
            {columns.map((c) => (
              <th key={c.key} className="px-4 py-2 font-medium">
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100">
          {rows.map((row) => (
            <tr key={rowKey(row)} className="hover:bg-slate-50/60">
              {columns.map((c) => (
                <td key={c.key} className={`px-4 py-2.5 ${c.className ?? ""}`}>
                  {c.render ? c.render(row) : ((row as Record<string, unknown>)[c.key] as React.ReactNode)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
