"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import dynamic from "next/dynamic";
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
import { specFromStruct } from "@/lib/chart-spec";
import NewProjectLauncher from "@/components/new-project-launcher";
import ConfirmDeleteDialog from "@/components/confirm-delete-dialog";

const InsightChart = dynamic(() => import("@/components/insight-chart"), {
  ssr: false,
  loading: () => <div className="insight-chart-skeleton" aria-hidden="true" />,
});

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

function InsightCard({ insight }: { insight: Insight }) {
  const [collapsed, setCollapsed] = useState(false);
  const spec = specFromStruct(insight.chartSpec);
  const panelId = `insight-chart-${insight.id}`;

  return (
    <article className="project-insight-card">
      <div className="project-insight-heading">
        <strong>{insight.headline}</strong>
        {spec && (
          <button
            type="button"
            className="chart-collapse"
            aria-expanded={!collapsed}
            aria-controls={panelId}
            onClick={() => setCollapsed((current) => !current)}
          >
            {collapsed ? "Expand" : "Collapse"}
          </button>
        )}
      </div>
      <p>{insight.explanation}</p>
      {!collapsed && spec && (
        <div id={panelId}>
          <InsightChart spec={spec} />
        </div>
      )}
    </article>
  );
}

function InsightsSection({ insights }: { insights: Insight[] }) {
  const [collapsed, setCollapsed] = useState(false);

  return (
    <section className="project-insights" id="findings" aria-labelledby="insights-heading">
      <div className="project-insights-heading">
        <span className="eyebrow" id="insights-heading">LATEST INSIGHTS</span>
        <button
          type="button"
          className="chart-collapse"
          aria-expanded={!collapsed}
          aria-controls="insights-panel"
          onClick={() => setCollapsed((current) => !current)}
        >
          {collapsed ? "Expand" : "Collapse"}
        </button>
      </div>
      <div id="insights-panel" className="project-insights-grid" hidden={collapsed}>
        {!collapsed && insights.map((insight) => (
          <InsightCard key={insight.id} insight={insight} />
        ))}
      </div>
    </section>
  );
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
  const [deletingProjectId, setDeletingProjectId] = useState("");
  const [projectToDelete, setProjectToDelete] = useState<Project | null>(null);
  const [deleteProjectError, setDeleteProjectError] = useState("");
  const [detail, setDetail] = useState<GetProjectResponse | null>(null);
  const [insights, setInsights] = useState<Insight[]>([]);
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [detailLoading, setDetailLoading] = useState(true);
  const [detailError, setDetailError] = useState("");
  const [forceRunPending, setForceRunPending] = useState(false);
  const [forceRunBaseId, setForceRunBaseId] = useState("");
  const [forceRunNotice, setForceRunNotice] = useState("");
  const [forceRunError, setForceRunError] = useState("");

  const sourceNames = useMemo(
    () => new Map(sources.map((source) => [source.id, source.name])),
    [sources],
  );

  const updateProjectData = useCallback((
    projectResponse: GetProjectResponse,
    currentInsights: Insight[],
    currentWorkflows: Workflow[],
  ) => {
    setDetail(projectResponse);
    setInsights(currentInsights);
    setWorkflows(currentWorkflows);
    const projectId = projectResponse.project?.id;
    if (projectId) {
      setProjects((current) => current.map((summary) => (
        summary.project?.id === projectId
          ? {
              ...summary,
              project: projectResponse.project ?? summary.project,
              latestRunStatus: projectResponse.latestRun?.status ?? RunStatus.UNSPECIFIED,
              findingCount: currentInsights.length,
            }
          : summary
      )));
    }
  }, []);

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
        updateProjectData(projectResponse, insightResponse.insights, workflowResponse.workflows);
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
  }, [selectedId, updateProjectData]);

  useEffect(() => {
    function sourceDeleted(event: Event) {
      const projectIds = (event as CustomEvent<string[]>).detail ?? [];
      if (!selectedId || !projectIds.includes(selectedId)) return;
      void Promise.all([
        getAnduinClient().getProject({ id: selectedId }),
        getAnduinClient().listInsights({ projectId: selectedId }),
        getAnduinClient().listWorkflows({ projectId: selectedId }),
      ])
        .then(([projectResponse, insightResponse, workflowResponse]) => {
          updateProjectData(projectResponse, insightResponse.insights, workflowResponse.workflows);
        })
        .catch((error: unknown) => {
          setDetailError(error instanceof Error ? error.message : "Could not refresh the project after source deletion.");
        });
    }
    window.addEventListener("anduin:source-deleted", sourceDeleted);
    return () => window.removeEventListener("anduin:source-deleted", sourceDeleted);
  }, [selectedId, updateProjectData]);

  useEffect(() => {
    if (!forceRunPending || !selectedId) return;

    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempts = 0;
    const refresh = async () => {
      try {
        const [projectResponse, insightResponse, workflowResponse] = await Promise.all([
          getAnduinClient().getProject({ id: selectedId }),
          getAnduinClient().listInsights({ projectId: selectedId }),
          getAnduinClient().listWorkflows({ projectId: selectedId }),
        ]);
        if (cancelled) return;
        updateProjectData(projectResponse, insightResponse.insights, workflowResponse.workflows);
        const latestRun = projectResponse.latestRun;
        if (latestRun && latestRun.id !== forceRunBaseId && latestRun.status !== RunStatus.RUNNING) {
          setForceRunPending(false);
          setForceRunNotice(latestRun.status === RunStatus.SUCCEEDED
            ? "Run complete; insights refreshed."
            : "The forced run failed. See the latest run status above.");
          return;
        }
        attempts += 1;
        if (attempts >= 180) {
          setForceRunPending(false);
          setForceRunNotice("The run was accepted but is taking longer than expected. Refresh the project to check its status.");
          return;
        }
      } catch {
        // Keep polling through brief transport errors; the accepted run continues server-side.
      }
      if (!cancelled) timer = setTimeout(() => void refresh(), 1000);
    };

    timer = setTimeout(() => void refresh(), 300);
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [forceRunBaseId, forceRunPending, selectedId, updateProjectData]);

  function selectProject(id: string) {
    if (id === selectedId) return;
    setForceRunPending(false);
    setForceRunNotice("");
    setForceRunError("");
    setDetailLoading(true);
    setDetailError("");
    setSelectedId(id);
  }

  async function forceRerun() {
    if (!selectedId) return;
    setForceRunPending(true);
    setForceRunBaseId(detail?.latestRun?.id ?? "");
    setForceRunNotice("Queueing run…");
    setForceRunError("");
    try {
      const response = await getAnduinClient().runWorkflow({
        projectId: selectedId,
        kind: WorkflowKind.STATISTICAL,
      });
      if (!response.accepted) {
        setForceRunPending(false);
        setForceRunError(response.message || "The workflow run was not accepted.");
      }
    } catch (error: unknown) {
      setForceRunPending(false);
      setForceRunError(error instanceof Error ? error.message : "Could not queue a workflow run.");
    }
  }

  function projectCreated(project: Project) {
    setForceRunPending(false);
    setForceRunNotice("");
    setForceRunError("");
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

  async function deleteProject(project: Project) {
    setDeletingProjectId(project.id);
    setDeleteProjectError("");
    try {
      await getAnduinClient().deleteProject({ id: project.id });
      setProjectToDelete(null);
      const remaining = projects.filter((summary) => summary.project?.id !== project.id);
      setProjects(remaining);
      if (selectedId === project.id) {
        const nextId = preferredProject(remaining);
        setDetail(null);
        setInsights([]);
        setWorkflows([]);
        setSelectedId(nextId);
        setDetailLoading(Boolean(nextId));
      }
    } catch (error) {
      setDeleteProjectError(error instanceof Error ? error.message : "Could not delete this project.");
    } finally {
      setDeletingProjectId("");
    }
  }

  const selectedProject = detail?.project;
  const selectedSummary = projects.find((summary) => summary.project?.id === selectedId);
  const latestRun = detail?.latestRun;
  const runStatus = latestRun?.status ?? selectedSummary?.latestRunStatus ?? RunStatus.UNSPECIFIED;
  const hasStatisticalWorkflow = detail?.activeWorkflows.some(
    (workflow) => workflow.kind === WorkflowKind.STATISTICAL,
  ) ?? false;

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
                <div
                  className={`project-list-item ${selected ? "is-selected" : ""}`}
                  key={project.id}
                  role="listitem"
                >
                  <span className={`project-list-status project-status-${summary.latestRunStatus}`} aria-hidden="true" />
                  <button
                    type="button"
                    className="project-list-select"
                    onClick={() => selectProject(project.id)}
                    aria-current={selected ? "true" : undefined}
                  >
                    <span className="project-list-copy">
                      <strong>{project.name}</strong>
                      <small>{project.goal}</small>
                      <span className="project-list-meta">
                        {project.sourceIds.length} {project.sourceIds.length === 1 ? "source" : "sources"}
                        {summary.findingCount ? ` · ${summary.findingCount} findings` : ""}
                      </span>
                    </span>
                  </button>
                  <span className="project-list-arrow" aria-hidden="true">›</span>
                  <button
                    type="button"
                    className="project-delete-button"
                    aria-label={`Delete ${project.name}`}
                    title={`Delete ${project.name}`}
                    disabled={Boolean(deletingProjectId)}
                    onClick={() => {
                      setDeleteProjectError("");
                      setProjectToDelete(project);
                    }}
                  >
                    {deletingProjectId === project.id ? "…" : "×"}
                  </button>
                </div>
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
                  <div className="project-detail-actions">
                    <span className={`project-run-badge project-run-${runStatus}`}>
                      <i aria-hidden="true" /> {runStatusLabel(runStatus)}
                    </span>
                    {hasStatisticalWorkflow && (
                      <button
                        type="button"
                        className="button button-outline project-force-run"
                        onClick={() => void forceRerun()}
                        disabled={forceRunPending || runStatus === RunStatus.RUNNING}
                      >
                        {forceRunPending ? "Running again…" : "Run again"}
                      </button>
                    )}
                  </div>
                </div>

                {(forceRunNotice || forceRunError) && (
                  <p className={forceRunError ? "project-force-run-error" : "project-force-run-notice"} role="status" aria-live="polite">
                    {forceRunError || forceRunNotice}
                  </p>
                )}

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
                  <InsightsSection key={selectedId} insights={insights} />
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
      <ConfirmDeleteDialog
        open={Boolean(projectToDelete)}
        itemName={projectToDelete?.name ?? ""}
        itemType="project"
        impact="Its workflow versions, run history, and insights will be permanently removed. Connected data sources will remain available."
        busy={Boolean(projectToDelete && deletingProjectId === projectToDelete.id)}
        error={deleteProjectError}
        onCancel={() => {
          if (!deletingProjectId) setProjectToDelete(null);
        }}
        onConfirm={() => projectToDelete && void deleteProject(projectToDelete)}
      />
    </section>
  );
}
