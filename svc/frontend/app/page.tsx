import ProjectBrowser from "@/components/project-browser";
import SourceManager from "@/components/source-manager";

function BrandMark() {
  return (
    <span className="brand-mark" aria-hidden="true">
      <svg viewBox="0 0 40 40">
        <circle className="river-disc" cx="20" cy="20" r="18" />
        <path className="river-current" d="M9 10c8-3 21-2 22 4 1 5-8 6-15 9-7 3-7 8 1 10 4 1 8 0 13-2" />
        <path className="river-shimmer" d="M10 10c8-2 17-1 19 3 1 3-4 5-10 7" />
        <circle className="river-source" cx="9" cy="10" r="1.5" />
      </svg>
    </span>
  );
}

export default function Home() {
  return (
    <div className="app-shell">
      <header className="app-header">
        <a className="brand" href="#project" aria-label="Anduin home">
          <BrandMark />
          <span className="brand-copy"><strong>anduin</strong><small>DATA FLOWS</small></span>
        </a>
        <nav className="top-nav" aria-label="Main navigation">
          <a href="#projects">Projects</a>
          <a href="#sources">Sources</a>
          <a href="#findings">Insights</a>
        </nav>
        <a className="button button-outline header-plan-link" href="#analysis-plan">Analysis methods <span aria-hidden="true">↗</span></a>
      </header>

      <main className="main-content" id="project">
        <div className="page-content">
          <section className="production-intro" aria-labelledby="production-intro-heading">
            <div>
              <span className="eyebrow">ANDUIN · DATA FLOWS</span>
              <h1 id="production-intro-heading">Turn connected data into clear signals.</h1>
              <p>Bring sources together around a question. Statistical workflows update as new data arrives, and every insight stays tied to its evidence.</p>
            </div>
            <a className="production-intro-link" href="#projects">View projects <span aria-hidden="true">↓</span></a>
          </section>

          <ProjectBrowser />

          <SourceManager />

          <footer className="page-footer">
            <span><span className="footer-mark">a</span> ANDUIN <span className="footer-divider">/</span> DATA FLOWS</span>
            <span>Statistical insights from connected sources.</span>
          </footer>
        </div>
      </main>
    </div>
  );
}
