"use client";

import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

type Row = { hour_unix: number; quantum_count: number; pq_adoption_percent: number };

export function PQHourlyChart({ data }: { data: Row[] }) {
  const sorted = [...data].reverse().map((r) => ({
    ...r,
    t: new Date(r.hour_unix * 1000).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit" }),
  }));
  return (
    <div className="glass-card h-80 p-4">
      <div className="mb-2 font-semibold text-doge-ink dark:text-amber-50">PQ mentions per indexed hour</div>
      <ResponsiveContainer width="100%" height="90%">
        <LineChart data={sorted}>
          <CartesianGrid strokeDasharray="3 3" opacity={0.2} />
          <XAxis dataKey="t" tick={{ fontSize: 11 }} />
          <YAxis tick={{ fontSize: 11 }} />
          <Tooltip />
          <Line type="monotone" dataKey="quantum_count" stroke="#8b5cf6" strokeWidth={2} dot={false} name="Quantum txs" />
          <Line type="monotone" dataKey="pq_adoption_percent" stroke="#34d399" strokeWidth={2} dot={false} name="% quantum" />
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}
