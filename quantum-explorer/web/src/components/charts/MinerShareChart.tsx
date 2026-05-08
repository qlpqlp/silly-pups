"use client";

import { Cell, Pie, PieChart, ResponsiveContainer, Tooltip } from "recharts";

const COLORS = ["#f2c94c", "#60a5fa", "#a78bfa", "#34d399", "#fb7185", "#f472b6", "#94a3b8"];

export function MinerShareChart({ rows }: { rows: { miner_short: string; blocks_mined: number }[] }) {
  const data = rows.slice(0, 8).map((r) => ({ name: r.miner_short || "unknown", value: r.blocks_mined }));
  return (
    <div className="glass-card h-80 p-4">
      <div className="mb-2 font-semibold text-doge-ink dark:text-amber-50">Where recent blocks were paid (coinbase)</div>
      <ResponsiveContainer width="100%" height="90%">
        <PieChart>
          <Pie dataKey="value" data={data} innerRadius={55} outerRadius={100} paddingAngle={3}>
            {data.map((_, i) => (
              <Cell key={i} fill={COLORS[i % COLORS.length]} />
            ))}
          </Pie>
          <Tooltip />
        </PieChart>
      </ResponsiveContainer>
    </div>
  );
}
