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
  const rows = state.templates.map((template) => {
    const tags = template.runner_tags.map((tag) => `<span class="tag">${tag}</span>`).join("");
    const approval = template.requires_ack ? "Required" : "Not required";
    return `
      <div class="row">
        <strong>${template.name}</strong>
        <span class="kind">${template.kind}</span>
        <span>${tags}</span>
        <span>${approval}</span>
      </div>`;
  });

  $("#templates").innerHTML = `
    <div class="row header">
      <span>Name</span>
      <span>Kind</span>
      <span>Runner tags</span>
      <span>Approval</span>
    </div>
    ${rows.join("")}`;
}

function renderRuns() {
  $("#runs").innerHTML = state.runs.map((run) => `
    <article class="run">
      <strong>${run.id}</strong>
      <span class="status ${run.status}">${run.status}</span>
      <p>${run.template_id} · ${new Date(run.started_at).toLocaleString()}</p>
    </article>
  `).join("");
}

function renderCapabilities() {
  $("#capabilities").innerHTML = state.capabilities.map((capability) => `
    <article class="capability">
      <div>
        <strong>${capability.name}</strong>
        <span class="status ${capability.status}">${capability.status}</span>
      </div>
      <p>${capability.description}</p>
    </article>
  `).join("");
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
