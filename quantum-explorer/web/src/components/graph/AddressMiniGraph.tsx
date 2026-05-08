"use client";

import dynamic from "next/dynamic";
import { useMemo } from "react";

const CytoscapeComponent = dynamic(() => import("react-cytoscapejs"), { ssr: false });

export function AddressMiniGraph({ center, peers }: { center: string; peers: string[] }) {
  const elements = useMemo(() => {
    const nodes = [{ data: { id: center, label: center.slice(0, 10) + "…" } }];
    const edges: { data: { id: string; source: string; target: string } }[] = [];
    peers.slice(0, 12).forEach((p, i) => {
      nodes.push({ data: { id: p, label: p.slice(0, 8) + "…" } });
      edges.push({ data: { id: `e${i}`, source: center, target: p } });
    });
    return [...nodes, ...edges];
  }, [center, peers]);

  return (
    <div className="glass-card h-[420px] w-full overflow-hidden p-2">
      <CytoscapeComponent
        elements={elements as never}
        style={{ width: "100%", height: "400px" }}
        cy={(cy) => {
          cy.layout({ name: "breadthfirst", directed: true, animate: true }).run();
        }}
        stylesheet={[
          {
            selector: "node",
            style: {
              label: "data(label)",
              "background-color": "#f2c94c",
              color: "#1c1410",
              "font-size": 10,
              "text-valign": "center",
              "text-halign": "center",
              width: 72,
              height: 72,
            },
          },
          {
            selector: "edge",
            style: {
              width: 2,
              "line-color": "#94a3b8",
              "target-arrow-color": "#94a3b8",
              "curve-style": "bezier",
              "target-arrow-shape": "triangle",
            },
          },
        ]}
      />
    </div>
  );
}
