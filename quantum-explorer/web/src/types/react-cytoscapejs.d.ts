declare module "react-cytoscapejs" {
  import type { CSSProperties, FC } from "react";
  import type { Core } from "cytoscape";

  export interface CytoscapeComponentProps {
    elements?: unknown;
    style?: CSSProperties;
    cy?: (cy: Core) => void;
    stylesheet?: unknown;
    [key: string]: unknown;
  }

  const CytoscapeComponent: FC<CytoscapeComponentProps>;
  export default CytoscapeComponent;
}
