/**
 * Standalone Agent → Model topology edge: an Agent created outside any
 * ensemble whose baseURL points at a Model CR's endpoint must appear as an
 * agent node with an "inference" edge from the model. Runs against stubbed
 * API responses — needs no cluster.
 */

const MODEL_ENDPOINT = "http://qwen.default.svc.cluster.local:8000/v1";

describe("topology: standalone agent → model edge", () => {
  beforeEach(() => {
    cy.intercept("GET", "**/api/v1/models*", [
      {
        metadata: { name: "qwen", namespace: "default" },
        spec: { inference: { serverType: "llama-cpp" } },
        status: { phase: "Ready", endpoint: MODEL_ENDPOINT },
      },
    ]).as("models");
    cy.intercept("GET", "**/api/v1/agents*", [
      {
        metadata: { name: "my-agent", namespace: "default" },
        spec: {
          agents: { default: { model: "qwen", baseURL: MODEL_ENDPOINT } },
          skills: [],
        },
      },
    ]).as("agents");
    cy.intercept("GET", "**/api/v1/ensembles*", []).as("ensembles");
    cy.intercept("GET", "**/api/v1/runs*", []).as("runs");
    cy.intercept("GET", "**/api/v1/providers/nodes*", []).as("providerNodes");
    cy.intercept("GET", "**/api/v1/gateway*", {}).as("gateway");
    cy.intercept("GET", "**/api/v1/density/**", { nodes: [] }).as("density");
  });

  it("renders the agent node and its inference edge from the model", () => {
    // Rides the URL-fragment auto-login.
    cy.visit("/topology#token=test-token", {
      onBeforeLoad(win) {
        win.localStorage.removeItem("sympozium_topology_positions");
      },
    });
    cy.wait(["@models", "@agents"]);

    cy.get('.react-flow__node[data-id="model-qwen"]').should("exist");
    cy.get('.react-flow__node[data-id="agent-my-agent"]')
      .should("exist")
      .and("contain.text", "my-agent")
      .and("contain.text", "qwen");
    cy.get('.react-flow__edge[data-id="e-agent-my-agent-model-qwen"]').should(
      "exist",
    );
  });
});
