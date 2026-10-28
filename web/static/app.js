const state = {
  projects: [],
  templates: [],
  runs: [],
  capabilities: [],
};

const $ = (selector) => document.querySelector(selector);

async function getJSON(path) {
  const response = await fetch(path, { headers: { Accept: "application/json" } });
  if (!response.ok) {
    throw new Error(`${response.status} ${response.statusText}`);
  }
  return response.json();
}

function renderPrincipal(principal) {
  $("#principal").textContent = `${principal.name} · ${principal.roles.join(", ")}`;
}

function renderMetrics() {
  $("#metric-projects").textContent = state.projects.length;
  $("#metric-templates").textContent = state.templates.length;
  $("#metric-approvals").textContent = state.templates.filter((template) => template.requires_ack).length;
  $("#metric-runs").textContent = state.runs.length;
  $("#metric-api").textContent = "OK";
}

function renderTemplates() {
  const header = document.createElement("div");
  header.className = "row header";
  for (const title of ["Name", "Kind", "Runner tags", "Approval"]) {
    header.append(textElement("span", title));
  }
  const rows = state.templates.map((template) => {
    const row = document.createElement("div");
    row.className = "row";
    const tags = document.createElement("span");
    tags.append(...template.runner_tags.map((tag) => textElement("span", tag, "tag")));
    row.append(textElement("strong", template.name), textElement("span", template.kind, "kind"), tags, textElement("span", template.requires_ack ? "Required" : "Not required"));
    return row;
  });
  $("#templates").replaceChildren(header, ...rows);
}

function textElement(tag, text, className = "") {
  const element = document.createElement(tag);
  element.className = className;
  element.textContent = text;
  return element;
}

function renderRuns() {
  $("#runs").replaceChildren(...state.runs.map((run) => {
    const row = document.createElement("article");
    row.className = "run";
    row.append(textElement("strong", run.id), textElement("span", run.status, `status ${run.status}`), textElement("p", `${run.template_id} · ${new Date(run.started_at).toLocaleString()}`));
    return row;
  }));
}

function renderCapabilities() {
  $("#capabilities").replaceChildren(...state.capabilities.map((capability) => {
    const row = document.createElement("article");
    row.className = "capability";
    const title = document.createElement("div");
    title.append(textElement("strong", capability.name), textElement("span", capability.status, `status ${capability.status}`));
    row.append(title, textElement("p", capability.description));
    return row;
  }));
}

async function refresh() {
  const [health, principal, projects, templates, runs, capabilities] = await Promise.all([
    getJSON("/api/v1/health"),
    getJSON("/api/v1/me"),
    getJSON("/api/v1/projects"),
    getJSON("/api/v1/templates"),
    getJSON("/api/v1/runs"),
    getJSON("/api/v1/capabilities"),
  ]);

  state.projects = projects.items;
  state.templates = templates.items;
  state.runs = runs.items;
  state.capabilities = capabilities.items;

  renderPrincipal(principal);
  renderMetrics();
  renderTemplates();
  renderRuns();
  renderCapabilities();
  $("#metric-api").textContent = health.status.toUpperCase();
}

document.querySelectorAll("nav button").forEach((button) => {
  button.addEventListener("click", () => {
    document.querySelectorAll("nav button").forEach((item) => item.classList.remove("active"));
    button.classList.add("active");
  });
});

$("#refresh").addEventListener("click", () => {
  refresh().catch((error) => {
    $("#metric-api").textContent = "ERR";
    console.error(error);
  });
});

refresh().catch((error) => {
  $("#metric-api").textContent = "ERR";
  console.error(error);
});
