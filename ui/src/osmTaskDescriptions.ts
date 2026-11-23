import rebuildContract from "virtual:osm-rebuild-contract";

// Import the contract itself so changing its inventory rebuilds the tooltip text.
export const osmTaskDescriptions: Readonly<Record<string, string>> = Object.fromEntries(
  [...rebuildContract.matchAll(/^\|\s*\d+\s*\|\s*`([^`]+)`\s*\|[^|]*\|\s*(.*?)\s*\|\s*$/gm)].map(
    ([, name, description]) => [name, description.replace(/`([^`]+)`/g, "$1")],
  ),
);
