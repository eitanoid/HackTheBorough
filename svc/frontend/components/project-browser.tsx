"use client";

import { useEffect, useMemo, useState } from "react";
import { create } from "@bufbuild/protobuf";
import {
  RunStatus,
  WorkflowKind,
  type GetProjectResponse,
  type Insight,
  type Project,
  type ProjectSummary,
  type Source,
  type Workflow,
  ProjectSummarySchema,
} from "@/gen/hacktheburgh/v1/service_pb";
import { getAnduinClient } from "@/lib/anduin-client";
import NewProjectLauncher from "@/components/new-project-launcher";

function runStatusLabel(status: RunStatus) {
  switch (status) {
    case RunStatus.SUCCEEDED:
      return "Run complete";
    case RunStatus.RUNNING:
      return "Run in progress";
    case RunStatus.FAILED:
      return "Run failed";
    default:
      return "No runs yet";
  }
}

function timestampLabel(timestamp?: { seconds: bigint; nanos: number }) {
  if (!timestamp) return "—";
  const date = new Date(Number(timestamp.seconds) * 1000 + timestamp.nanos / 1_000_000);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString();
}

function workflowKindLabel(kind: WorkflowKind) {
  switch (kind) {
    case WorkflowKind.STATISTICAL:
      return "Statistical";
    case WorkflowKind.INVESTIGATION:
      return "Investigation";
    default:
      return "Workflow";
  }
}

function preferredProject(summaries: ProjectSummary[]) {
  return summaries[0]?.project?.id ?? "";
}

export default function ProjectBrowser() {
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [sources, setSources] = useState<Source[]>([]);
  const [selectedId, setSelectedId] = useState("");
  const [projectListLoading, setProjectListLoading] = useState(true);
  const [projectListError, setProjectListError] = useState("");
  const [detail, setDetail] = useState<GetProjectResponse | null>(null);
  const [insights, setInsights] = useState<Insight[]>([]);
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [detailLoading, setDetailLoading] = useState(true);
  const [detailError, setDetailError] = useState("");

  const sourceNames = useMemo(
    () => new Map(sources.map((source) => [source.id, source.name])),
    [sources],
  );

  useEffect(() => {
    let cancelled = false;
    const client = getAnduinClient();
    void client.listProjects({})
      .then((response) => {
        if (cancelled) return;
        const currentProjects = response.projects.filter((summary) => summary.project?.id);
        setProjects(currentProjects);
        setSelectedId((current) => current || preferredProject(currentProjects));
        setProjectListError("");
      })
      .catch((error: unknown) => {
        if (!cancelled) setProjectListError(error instanceof Error ? error.message : "Could not load projects.");
      })
      .finally(() => {
        if (!cancelled) setProjectListLoading(false);
      });
    void client.listSources({})
      .then((response) => {
        if (!cancelled) setSources(response.sources);
      })
      .catch(() => {
        // Project metadata remains useful even if source names are temporarily unavailable.
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!selectedId) return;

    let cancelled = false;
    void Promise.all([
      getAnduinClient().getProject({ id: selectedId }),
      getAnduinClient().listInsights({ projectId: selectedId }),
      getAnduinClient().listWorkflows({ projectId: selectedId }),
    ])
      .then(([projectResponse, insightResponse, workflowResponse]) => {
        if (cancelled) return;
        setDetail(projectResponse);
        setInsights(insightResponse.insights);
        setWorkflows(workflowResponse.workflows);
        setDetailError("");
      })
      .catch((error: unknown) => {
        if (!cancelled) setDetailError(error instanceof Error ? error.message : "Could not load project details.");
      })
      .finally(() => {
        if (!cancelled) setDetailLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [selectedId]);

  function selectProject(id: string) {
    if (id === selectedId) return;
    setDetailLoading(true);
    setDetailError("");
    setSelectedId(id);
  }

  function projectCreated(project: Project) {
    const summary = create(ProjectSummarySchema, {
      project,
      latestRunStatus: RunStatus.UNSPECIFIED,
      findingCount: 0,
    });
    setProjects((current) => [summary, ...current.filter((item) => item.project?.id !== project.id)]);
    setDetailLoading(true);
    setDetailError("");
    setSelectedId(project.id);
  }

  const selectedProject = detail?.project;
  const selectedSummary = projects.find((summary) => summary.project?.id === selectedId);
  const latestRun = detail?.latestRun;
  const runStatus = latestRun?.status ?? selectedSummary?.latestRunStatus ?? RunStatus.UNSPECIFIED;

  return (
    <section className="projects-panel" id="projects" aria-labelledby="projects-panel-heading">
      <div className="projects-panel-heading">
        <div>
          <span className="eyebrow">WORKSPACE</span>
          <h2 id="projects-panel-heading">Your projects</h2>
        </div>
        <NewProjectLauncher onCreated={projectCreated} />
      </div>

      {projectListError && (
        <div className="project-form-error" role="alert">{projectListError}</div>
      )}

      {projectListLoading ? (
        <div className="projects-empty"><i className="project-spinner" /> Loading projects…</div>
      ) : projects.length === 0 ? (
        <div className="projects-empty">
          <strong>No projects yet</strong>
          <span>Create a project to connect sources to a question you want to explore.</span>
        </div>
      ) : (
        <div className="projects-browser-grid">
          <div className="projects-list" role="list" aria-label="Saved projects">
            {projects.map((summary) => {
              const project = summary.project;
              if (!project) return null;
              const selected = project.id === selectedId;
              return (
                <button
                  type="button"
                  className={`project-list-item ${selected ? "is-selected" : ""}`}
                  key={project.id}
                  onClick={() => selectProject(project.id)}
                  aria-current={selected ? "true" : undefined}
                >
                  <span className={`project-list-status project-status-${summary.latestRunStatus}`} aria-hidden="true" />
                  <span className="project-list-copy">
                    <strong>{project.name}</strong>
                    <small>{project.goal}</small>
                    <span className="project-list-meta">
                      {project.sourceIds.length} {project.sourceIds.length === 1 ? "source" : "sources"}
                      {summary.findingCount ? ` · ${summary.findingCount} findings` : ""}
                    </span>
                  </span>
                  <span className="project-list-arrow" aria-hidden="true">›</span>
                </button>
              );
            })}
          </div>

          <div className="project-detail-card">
            {detailLoading ? (
              <div className="projects-empty"><i className="project-spinner" /> Loading project details…</div>
            ) : detailError ? (
              <div className="project-form-error" role="alert">{detailError}</div>
            ) : selectedProject ? (
              <>
                <div className="project-detail-heading">
                  <div>
                    <span className="eyebrow">SELECTED PROJECT</span>
                    <h3>{selectedProject.name}</h3>
                    <p>{selectedProject.goal}</p>
                  </div>
                  <span className={`project-run-badge project-run-${runStatus}`}>
                    <i aria-hidden="true" /> {runStatusLabel(runStatus)}
                  </span>
                </div>

                <div className="project-detail-stats">
                  <div><strong>{selectedProject.sourceIds.length}</strong><span>Sources</span></div>
                  <div><strong>{selectedSummary?.findingCount ?? insights.length}</strong><span>Findings</span></div>
                  <div><strong>{workflows.length}</strong><span>Workflows</span></div>
                </div>

                <div className="project-linked-sources">
                  <span className="eyebrow">LINKED SOURCES</span>
                  <div>
                    {selectedProject.sourceIds.map((sourceId) => (
                      <span className="project-source-chip" key={sourceId}>
                        {sourceNames.get(sourceId) ?? `${sourceId.slice(0, 8)}…`}
                      </span>
                    ))}
                  </div>
                </div>

                {detail?.funnel && (
                  <div className="project-funnel-row">
                    <span><strong>{detail.funnel.testsRun}</strong> tests</span>
                    <span><strong>{detail.funnel.passedFdr}</strong> passed FDR</span>
                    <span><strong>{detail.funnel.passedStability}</strong> stable</span>
                  </div>
                )}

                {latestRun && (
                  <div className="project-latest-run">
                    <span className="eyebrow">LATEST RUN</span>
                    <p>{latestRun.error || `${runStatusLabel(latestRun.status)} · updated ${timestampLabel(latestRun.finishedAt ?? latestRun.startedAt)}`}</p>
                  </div>
                )}

                {workflows.length > 0 && (
                  <div className="project-workflows">
                    <span className="eyebrow">WORKFLOW VERSIONS</span>
                    {workflows.map((workflow) => (
                      <div className="project-workflow-row" key={workflow.id}>
                        <span><strong>{workflowKindLabel(workflow.kind)}{workflow.isActive ? " · Active" : ""}</strong><small>Version {workflow.version}</small></span>
                        <span>{workflow.rationale || workflow.insightPrompt}</span>
                      </div>
                    ))}
                  </div>
                )}

                {insights.length > 0 ? (
                  <div className="project-insights" id="findings">
                    <span className="eyebrow">LATEST INSIGHTS</span>
                    {insights.map((insight) => (
                      <article className="project-insight-card" key={insight.id}>
                        <strong>{insight.headline}</strong>
                        <p>{insight.explanation}</p>
                      </article>
                    ))}
                  </div>
                ) : (
                  <p className="project-no-insights" id="findings">
                    {workflows.length ? "No insights have been published for this project yet." : "This project is saved; workflow generation has not been started."}
                  </p>
                )}
              </>
            ) : (
              <div className="projects-empty">Select a project to view its current data.</div>
            )}
          </div>
        </div>
      )}
    </section>
  );
}
