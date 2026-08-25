import { defineConfig } from "astro/config";
import mermaid from "astro-mermaid";
import starlight from "@astrojs/starlight";

export default defineConfig({
  site: "https://safelane-dev.github.io",
  base: "/safelane",
  integrations: [
    mermaid({ enableLog: false }),
    starlight({
      title: "SafeLane",
      description: "Autonomous progressive delivery for coding agents on Argo Rollouts.",
      favicon: "/favicon.png",
      customCss: ["./src/styles/custom.css"],
      components: {
        SiteTitle: "./src/components/SiteTitle.astro"
      },
      social: [
        {
          icon: "github",
          label: "GitHub",
          href: "https://github.com/safelane-dev/safelane"
        }
      ],
      sidebar: [
        { label: "Start Here", items: [
          { label: "What is SafeLane?", slug: "start-here/introduction" },
          { label: "Quick Start", slug: "start-here/quick-start" },
          { label: "Installation", slug: "start-here/installation" }
        ] },
        { label: "How It Works", collapsed: true, items: [
          { label: "Release Lifecycle", slug: "concepts/release-lifecycle" },
          { label: "Assessment & Recommendation", slug: "concepts/assessment" },
          { label: "Approval Boundary", slug: "concepts/boundary" },
          { label: "Release Lanes", slug: "concepts/release-policy" },
          { label: "History & Proof", slug: "concepts/record-and-proof" }
        ] },
        { label: "Guides", collapsed: true, items: [
          { label: "Register an Application", slug: "guides/setting-up" },
          { label: "Run a Release", slug: "guides/release-end-to-end" },
          { label: "Monitor & Control", slug: "guides/rollout-recovery" },
          { label: "Run the Local Demo", slug: "guides/local-demo" },
          { label: "Use the Agent Skill", slug: "guides/agent-skill" }
        ] },
        { label: "Reference", collapsed: true, items: [
          { label: "CLI", slug: "reference/cli" },
          { label: "Configuration", slug: "reference/configuration" },
          { label: "Compatibility", slug: "reference/compatibility" },
          { label: "Exit Codes", slug: "reference/exit-codes" },
          { label: "Release Data", slug: "reference/release-record" },
          { label: "Roadmap", slug: "roadmap" }
        ] }
      ]
    })
  ]
});
